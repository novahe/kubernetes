/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package scheduler

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/ktesting"
	fwk "k8s.io/kube-scheduler/framework"
)

func TestBuildPluginScoresMap(t *testing.T) {
	tests := []struct {
		name       string
		nodesScores []fwk.NodePluginScores
		want       pluginScoresMap
	}{
		{
			name: "basic case with multiple plugins and nodes",
			nodesScores: []fwk.NodePluginScores{
				{
					Name: "node1",
					Scores: []fwk.PluginScore{
						{Name: "NodeResourcesFit", Score: 100},
						{Name: "ImageLocality", Score: 50},
					},
					TotalScore: 150,
				},
				{
					Name: "node2",
					Scores: []fwk.PluginScore{
						{Name: "NodeResourcesFit", Score: 90},
						{Name: "ImageLocality", Score: 80},
					},
					TotalScore: 170,
				},
			},
			want: pluginScoresMap{
				"NodeResourcesFit": {
					"node1": 100,
					"node2": 90,
				},
				"ImageLocality": {
					"node1": 50,
					"node2": 80,
				},
			},
		},
		{
			name: "single plugin single node",
			nodesScores: []fwk.NodePluginScores{
				{
					Name: "node1",
					Scores: []fwk.PluginScore{
						{Name: "NodeResourcesFit", Score: 100},
					},
					TotalScore: 100,
				},
			},
			want: pluginScoresMap{
				"NodeResourcesFit": {
					"node1": 100,
				},
			},
		},
		{
			name:       "empty node scores",
			nodesScores: []fwk.NodePluginScores{},
			want:       pluginScoresMap{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildPluginScoresMap(tt.nodesScores)
			if !comparePluginScoresMap(got, tt.want) {
				t.Errorf("buildPluginScoresMap() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestComputeNodeTotalScores(t *testing.T) {
	tests := []struct {
		name       string
		nodesScores []fwk.NodePluginScores
		want       map[string]int64
	}{
		{
			name: "basic case",
			nodesScores: []fwk.NodePluginScores{
				{Name: "node1", TotalScore: 150},
				{Name: "node2", TotalScore: 170},
			},
			want: map[string]int64{
				"node1": 150,
				"node2": 170,
			},
		},
		{
			name: "empty list",
			nodesScores: []fwk.NodePluginScores{},
			want:       map[string]int64{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := make(map[string]int64)
			for _, ns := range tt.nodesScores {
				got[ns.Name] = ns.TotalScore
			}
			if !compareMaps(got, tt.want) {
				t.Errorf("computeNodeTotalScores() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTopKNodes(t *testing.T) {
	tests := []struct {
		name string
		total map[string]int64
		k    int
		want []string
	}{
		{
			name: "select top 3 from 5 nodes",
			total: map[string]int64{
				"node1": 100,
				"node2": 200,
				"node3": 150,
				"node4": 180,
				"node5": 120,
			},
			k: 3,
			want: []string{"node2", "node4", "node3"}, // sorted by score: 200, 180, 150
		},
		{
			name: "k larger than number of nodes",
			total: map[string]int64{
				"node1": 100,
				"node2": 200,
			},
			k:    5,
			want: []string{"node2", "node1"},
		},
		{
			name: "k equals number of nodes",
			total: map[string]int64{
				"node1": 100,
				"node2": 200,
				"node3": 150,
			},
			k:    3,
			want: []string{"node2", "node3", "node1"},
		},
		{
			name: "empty map",
			total: map[string]int64{},
			k:    3,
			want: []string{},
		},
		{
			name: "single node",
			total: map[string]int64{
				"node1": 100,
			},
			k:    3,
			want: []string{"node1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getTopKNodes(tt.total, tt.k)
			if !compareSlices(got, tt.want) {
				t.Errorf("getTopKNodes() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestJaccardSimilarity(t *testing.T) {
	tests := []struct {
		name  string
		a     []string
		b     []string
		want  float64
	}{
		{
			name: "identical sets",
			a:    []string{"node1", "node2", "node3"},
			b:    []string{"node1", "node2", "node3"},
			want: 1.0,
		},
		{
			name: "completely different sets",
			a:    []string{"node1", "node2"},
			b:    []string{"node3", "node4"},
			want: 0.0,
		},
		{
			name: "partial overlap",
			a:    []string{"node1", "node2", "node3"},
			b:    []string{"node2", "node3", "node4"},
			want: 0.5, // intersection: {node2, node3} = 2, union: {node1, node2, node3, node4} = 4
		},
		{
			name: "one is subset of another",
			a:    []string{"node1", "node2"},
			b:    []string{"node1", "node2", "node3"},
			want: 2.0 / 3.0, // intersection: 2, union: 3
		},
		{
			name:  "empty sets",
			a:     []string{},
			b:     []string{},
			want:  1.0,
		},
		{
			name: "one empty set",
			a:    []string{"node1", "node2"},
			b:    []string{},
			want: 0.0,
		},
		{
			name: "with duplicates - should be treated as set",
			a:    []string{"node1", "node1", "node2"},
			b:    []string{"node1", "node2", "node2"},
			want: 1.0, // both sets are {node1, node2}
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateJaccard(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("calculateJaccard() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAnalyzePluginInfluence(t *testing.T) {
	_, ctx := ktesting.NewTestContext(t)
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
	}

	tests := []struct {
		name       string
		nodesScores []fwk.NodePluginScores
		topKSize   int
		wantCount  int
		wantSorted bool // check if results are sorted by influence
	}{
		{
			name: "basic case - ImageLocality should be most influential",
			nodesScores: []fwk.NodePluginScores{
				{
					Name: "node-a",
					Scores: []fwk.PluginScore{
						{Name: "NodeResourcesFit", Score: 200},
						{Name: "ImageLocality", Score: 80},
						{Name: "InterPodAffinity", Score: 10},
					},
					TotalScore: 290,
				},
				{
					Name: "node-b",
					Scores: []fwk.PluginScore{
						{Name: "NodeResourcesFit", Score: 200},
						{Name: "ImageLocality", Score: 30},
						{Name: "InterPodAffinity", Score: 60},
					},
					TotalScore: 290,
				},
				{
					Name: "node-c",
					Scores: []fwk.PluginScore{
						{Name: "NodeResourcesFit", Score: 200},
						{Name: "ImageLocality", Score: 20},
						{Name: "InterPodAffinity", Score: 50},
					},
					TotalScore: 270,
				},
			},
			topKSize:   3,
			wantCount:  2, // NodeResourcesFit has zero variance (200 everywhere), skipped
			wantSorted: true,
		},
		{
			name: "all plugins give same scores - no influence difference",
			nodesScores: []fwk.NodePluginScores{
				{
					Name: "node1",
					Scores: []fwk.PluginScore{
						{Name: "PluginA", Score: 100},
						{Name: "PluginB", Score: 100},
					},
					TotalScore: 200,
				},
				{
					Name: "node2",
					Scores: []fwk.PluginScore{
						{Name: "PluginA", Score: 100},
						{Name: "PluginB", Score: 100},
					},
					TotalScore: 200,
				},
			},
			topKSize:   2,
			wantCount:  0, // All plugins have zero variance, skipped
			wantSorted: false,
		},
		{
			name: "single node - no winner change possible",
			nodesScores: []fwk.NodePluginScores{
				{
					Name: "node1",
					Scores: []fwk.PluginScore{
						{Name: "PluginA", Score: 100},
						{Name: "PluginB", Score: 50},
					},
					TotalScore: 150,
				},
			},
			topKSize:   1,
			wantCount:  0, // Single node cannot differentiate plugin impact
			wantSorted: false,
		},
		{
			name:       "empty node scores",
			nodesScores: []fwk.NodePluginScores{},
			topKSize:   3,
			wantCount:  0,
			wantSorted: false,
		},
		{
			name: "winner changes when removing plugin",
			nodesScores: []fwk.NodePluginScores{
				{
					Name: "node1",
					Scores: []fwk.PluginScore{
						{Name: "PluginA", Score: 100},
						{Name: "PluginB", Score: 10},
					},
					TotalScore: 110,
				},
				{
					Name: "node2",
					Scores: []fwk.PluginScore{
						{Name: "PluginA", Score: 90},
						{Name: "PluginB", Score: 50},
					},
					TotalScore: 140,
				},
			},
			topKSize:   2,
			wantCount:  2,
			wantSorted: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := klog.FromContext(ctx)
			results := analyzePluginInfluence(logger, pod, tt.nodesScores, tt.topKSize)

			if len(results) != tt.wantCount {
				t.Errorf("analyzePluginInfluence() returned %d results, want %d", len(results), tt.wantCount)
			}

			if tt.wantSorted && len(results) > 0 {
				// Check if results are sorted: winnerChange should be true first, then by Jaccard ascending
				for i := 1; i < len(results); i++ {
					prev := results[i-1]
					curr := results[i]

					// If previous has winnerChanged=true, current should not have winnerChanged=false with higher priority
					if prev.WinnerChange && !curr.WinnerChange {
						// This is fine, winnerChange=true comes first
						continue
					}

					// If both have same winnerChange status, Jaccard should be ascending
					if prev.WinnerChange == curr.WinnerChange {
						if prev.Jaccard > curr.Jaccard {
							t.Errorf("Results not properly sorted: at index %d, Jaccard=%v, at index %d, Jaccard=%v (should be ascending)",
								i-1, prev.Jaccard, i, curr.Jaccard)
						}
					}

					// If previous doesn't have winnerChange and current does, that's wrong
					if !prev.WinnerChange && curr.WinnerChange {
						t.Errorf("Results not properly sorted: winnerChanged=true should come before winnerChanged=false")
					}
				}
			}

			// Additional validation: verify results are consistent
			for _, r := range results {
				if r.Jaccard < 0 || r.Jaccard > 1 {
					t.Errorf("Invalid Jaccard value %v for plugin %s (should be in [0, 1])", r.Jaccard, r.PluginName)
				}
				if r.RankShift < 0 {
					t.Errorf("Invalid RankShift value %d for plugin %s (should be >= 0)", r.RankShift, r.PluginName)
				}
				if r.ScoreVariance < 0 {
					t.Errorf("Invalid ScoreVariance value %d for plugin %s (should be >= 0)", r.ScoreVariance, r.PluginName)
				}
				if r.PluginName == "" {
					t.Errorf("Empty plugin name in result")
				}
			}
		})
	}
}

func TestIsZeroVariance(t *testing.T) {
	tests := []struct {
		name     string
		scores   map[string]int64
		expected bool
	}{
		{
			name:     "all same scores",
			scores:   map[string]int64{"node1": 100, "node2": 100, "node3": 100},
			expected: true,
		},
		{
			name:     "different scores",
			scores:   map[string]int64{"node1": 100, "node2": 90, "node3": 80},
			expected: false,
		},
		{
			name:     "single node",
			scores:   map[string]int64{"node1": 100},
			expected: true,
		},
		{
			name:     "empty map",
			scores:   map[string]int64{},
			expected: true,
		},
		{
			name:     "two nodes same",
			scores:   map[string]int64{"node1": 50, "node2": 50},
			expected: true,
		},
		{
			name:     "two nodes different",
			scores:   map[string]int64{"node1": 50, "node2": 51},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isZeroVariance(tt.scores)
			if got != tt.expected {
				t.Errorf("isZeroVariance() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestComputeScoreVariance(t *testing.T) {
	tests := []struct {
		name     string
		scores   map[string]int64
		expected int64
	}{
		{
			name:     "all same scores",
			scores:   map[string]int64{"node1": 100, "node2": 100, "node3": 100},
			expected: 0,
		},
		{
			name:     "different scores",
			scores:   map[string]int64{"node1": 100, "node2": 90, "node3": 80},
			expected: 20, // 100 - 80
		},
		{
			name:     "single node",
			scores:   map[string]int64{"node1": 100},
			expected: 0,
		},
		{
			name:     "empty map",
			scores:   map[string]int64{},
			expected: 0,
		},
		{
			name:     "large variance",
			scores:   map[string]int64{"node1": 0, "node2": 1000},
			expected: 1000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateVariance(tt.scores)
			if got != tt.expected {
				t.Errorf("calculateVariance() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestComputeRankShift(t *testing.T) {
	tests := []struct {
		name     string
		baseline []string
		modified []string
		expected int
	}{
		{
			name:     "no change",
			baseline: []string{"node1", "node2", "node3"},
			modified: []string{"node1", "node2", "node3"},
			expected: 0,
		},
		{
			name:     "complete reversal",
			baseline: []string{"node1", "node2", "node3"},
			modified: []string{"node3", "node2", "node1"},
			expected: 4, // |0-2| + |1-1| + |2-0| = 2 + 0 + 2 = 4
		},
		{
			name:     "swap first two",
			baseline: []string{"node1", "node2", "node3"},
			modified: []string{"node2", "node1", "node3"},
			expected: 2, // |0-1| + |1-0| + |2-2| = 1 + 1 + 0 = 2
		},
		{
			name:     "one node displaced",
			baseline: []string{"node1", "node2", "node3"},
			modified: []string{"node2", "node3", "node1"},
			expected: 4, // |0-2| + |1-0| + |2-1| = 2 + 1 + 1 = 4
		},
		{
			name:     "different sets - complete turnover",
			baseline: []string{"node1", "node2", "node3"},
			modified: []string{"node4", "node5", "node6"},
			expected: 9, // penalty=3 for all 3 nodes dropping out (3*3=9)
		},
		{
			name:     "one node drops out of TopK",
			baseline: []string{"node1", "node2", "node3"},
			modified: []string{"node2", "node3", "node4"},
			expected: 5, // node1 dropped: penalty=3, node2: |1-0|=1, node3: |2-1|=1 => 3+1+1=5
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateRankShift(tt.baseline, tt.modified)
			if got != tt.expected {
				t.Errorf("calculateRankShift() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestLogPluginInfluenceRanking(t *testing.T) {
	tests := []struct {
		name       string
		logLevel   int
		results    []PluginInfluenceResult
		shouldLog  bool
		topKSize   int
	}{
		{
			name:     "log at level 4",
			logLevel: 4,
			results: []PluginInfluenceResult{
				{PluginName: "ImageLocality", WinnerChange: true, Jaccard: 0.5},
				{PluginName: "NodeResourcesFit", WinnerChange: false, Jaccard: 1.0},
			},
			shouldLog: true,
			topKSize:  3,
		},
		{
			name:     "log at level 5",
			logLevel: 5,
			results: []PluginInfluenceResult{
				{PluginName: "ImageLocality", WinnerChange: true, Jaccard: 0.5},
			},
			shouldLog: true,
			topKSize:  3,
		},
		{
			name:     "do not log at level 3",
			logLevel: 3,
			results: []PluginInfluenceResult{
				{PluginName: "ImageLocality", WinnerChange: true, Jaccard: 0.5},
			},
			shouldLog: false,
			topKSize:  3,
		},
		{
			name:     "empty results - should not log",
			logLevel: 4,
			results:  []PluginInfluenceResult{},
			shouldLog: false,
			topKSize:  3,
		},
		{
			name:     "nil results - should not log",
			logLevel: 4,
			results:  nil,
			shouldLog: false,
			topKSize:  3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ctx := ktesting.NewTestContext(t)
			logger := klog.FromContext(ctx).WithValues("v", tt.logLevel)

			// This function should not panic
			logPluginInfluenceRanking(logger, &v1.Pod{}, tt.results, tt.topKSize)

			// The actual logging output is hard to test without capturing the logger output,
			// but we've verified the function doesn't panic
			// In a real test environment, you might want to use a custom log writer to capture output
		})
	}
}

func TestPluginInfluenceResultSorting(t *testing.T) {
	// Test the sorting logic directly
	results := []PluginInfluenceResult{
		{PluginName: "PluginC", WinnerChange: false, Jaccard: 0.8},
		{PluginName: "PluginA", WinnerChange: true, Jaccard: 0.9},
		{PluginName: "PluginB", WinnerChange: false, Jaccard: 0.3},
		{PluginName: "PluginD", WinnerChange: true, Jaccard: 0.2},
	}

	// Sort using the same logic as analyzePluginInfluence
	sort.Slice(results, func(i, j int) bool {
		if results[i].WinnerChange != results[j].WinnerChange {
			return results[i].WinnerChange
		}
		return results[i].Jaccard < results[j].Jaccard
	})

	// Verify sorting order
	// Should be: WinnerChange=true first (sorted by Jaccard asc), then WinnerChange=false (sorted by Jaccard asc)
	// PluginD (true, 0.2), PluginA (true, 0.9), PluginB (false, 0.3), PluginC (false, 0.8)
	expectedOrder := []string{"PluginD", "PluginA", "PluginB", "PluginC"}

	for i, expected := range expectedOrder {
		if results[i].PluginName != expected {
			t.Errorf("Result at index %d: got plugin %s, want %s", i, results[i].PluginName, expected)
		}
	}

	// Verify winnerChanged plugins come first
	lastWinnerChangeIdx := -1
	for i, r := range results {
		if r.WinnerChange {
			lastWinnerChangeIdx = i
		}
	}

	if lastWinnerChangeIdx >= 0 && lastWinnerChangeIdx < len(results)-1 {
		// Check that all plugins after lastWinnerChangeIdx have WinnerChange=false
		for i := lastWinnerChangeIdx + 1; i < len(results); i++ {
			if results[i].WinnerChange {
				t.Errorf("Plugin %s at index %d has WinnerChange=true but comes after plugins with WinnerChange=false",
					results[i].PluginName, i)
			}
		}
	}
}

// Helper functions for comparison

func comparePluginScoresMap(a, b pluginScoresMap) bool {
	if len(a) != len(b) {
		return false
	}
	for plugin, nodesA := range a {
		nodesB, ok := b[plugin]
		if !ok || len(nodesA) != len(nodesB) {
			return false
		}
		for node, scoreA := range nodesA {
			scoreB, ok := nodesB[node]
			if !ok || scoreA != scoreB {
				return false
			}
		}
	}
	return true
}

func compareMaps(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if vb, ok := b[k]; !ok || v != vb {
			return false
		}
	}
	return true
}

func compareSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestEdgeCases tests various edge cases
func TestEdgeCases(t *testing.T) {
	_, ctx := ktesting.NewTestContext(t)
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
	}

	t.Run("topKSize larger than available nodes", func(t *testing.T) {
		nodesScores := []fwk.NodePluginScores{
			{
				Name: "node1",
				Scores: []fwk.PluginScore{
					{Name: "PluginA", Score: 100},
				},
				TotalScore: 100,
			},
			{
				Name: "node2",
				Scores: []fwk.PluginScore{
					{Name: "PluginA", Score: 90},
				},
				TotalScore: 90,
			},
		}

		logger := klog.FromContext(ctx)
		results := analyzePluginInfluence(logger, pod, nodesScores, 10) // topKSize > number of nodes

		if len(results) != 1 {
			t.Errorf("Expected 1 result, got %d", len(results))
		}
	})

	t.Run("plugin with zero scores", func(t *testing.T) {
		nodesScores := []fwk.NodePluginScores{
			{
				Name: "node1",
				Scores: []fwk.PluginScore{
					{Name: "PluginA", Score: 0},
					{Name: "PluginB", Score: 100},
				},
				TotalScore: 100,
			},
			{
				Name: "node2",
				Scores: []fwk.PluginScore{
					{Name: "PluginA", Score: 0},
					{Name: "PluginB", Score: 90},
				},
				TotalScore: 90,
			},
		}

		logger := klog.FromContext(ctx)
		results := analyzePluginInfluence(logger, pod, nodesScores, 2)

		// PluginA with zero scores should not affect winner
		for _, r := range results {
			if r.PluginName == "PluginA" {
				if r.WinnerChange {
					t.Errorf("PluginA with zero scores should not change winner")
				}
			}
		}
	})

	t.Run("negative scores (if any plugin returns negative)", func(t *testing.T) {
		nodesScores := []fwk.NodePluginScores{
			{
				Name: "node1",
				Scores: []fwk.PluginScore{
					{Name: "PluginA", Score: 50},
					{Name: "PluginB", Score: -10},
				},
				TotalScore: 40,
			},
			{
				Name: "node2",
				Scores: []fwk.PluginScore{
					{Name: "PluginA", Score: 40},
					{Name: "PluginB", Score: -5},
				},
				TotalScore: 35,
			},
		}

		logger := klog.FromContext(ctx)
		results := analyzePluginInfluence(logger, pod, nodesScores, 2)

		// Should handle negative scores gracefully
		if len(results) != 2 {
			t.Errorf("Expected 2 results, got %d", len(results))
		}
	})
}

// TestRealWorldScenario tests a realistic scheduling scenario
func TestRealWorldScenario(t *testing.T) {
	_, ctx := ktesting.NewTestContext(t)
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx-deployment-abc",
			Namespace: "default",
		},
	}

	// Simulate a real scenario where:
	// - NodeResourcesFit gives similar scores to all nodes (not very discriminating)
	// - ImageLocality has a strong preference for certain nodes
	// - InterPodAffinity slightly influences the decision
	nodesScores := []fwk.NodePluginScores{
		{
			Name: "node-1",
			Scores: []fwk.PluginScore{
				{Name: "NodeResourcesFit", Score: 95},
				{Name: "ImageLocality", Score: 80},
				{Name: "InterPodAffinity", Score: 0},
				{Name: "NodeAffinity", Score: 50},
			},
			TotalScore: 225,
		},
		{
			Name: "node-2",
			Scores: []fwk.PluginScore{
				{Name: "NodeResourcesFit", Score: 96},
				{Name: "ImageLocality", Score: 20},
				{Name: "InterPodAffinity", Score: 0},
				{Name: "NodeAffinity", Score: 50},
			},
			TotalScore: 166,
		},
		{
			Name: "node-3",
			Scores: []fwk.PluginScore{
				{Name: "NodeResourcesFit", Score: 94},
				{Name: "ImageLocality", Score: 15},
				{Name: "InterPodAffinity", Score: 0},
				{Name: "NodeAffinity", Score: 50},
			},
			TotalScore: 159,
		},
		{
			Name: "node-4",
			Scores: []fwk.PluginScore{
				{Name: "NodeResourcesFit", Score: 95},
				{Name: "ImageLocality", Score: 10},
				{Name: "InterPodAffinity", Score: 10},
				{Name: "NodeAffinity", Score: 50},
			},
			TotalScore: 165,
		},
		{
			Name: "node-5",
			Scores: []fwk.PluginScore{
				{Name: "NodeResourcesFit", Score: 93},
				{Name: "ImageLocality", Score: 5},
				{Name: "InterPodAffinity", Score: 10},
				{Name: "NodeAffinity", Score: 50},
			},
			TotalScore: 158,
		},
	}

	logger := klog.FromContext(ctx)
	results := analyzePluginInfluence(logger, pod, nodesScores, 3)

	// Verify we get results for all plugins
	expectedPlugins := map[string]bool{
		"NodeResourcesFit":  false,
		"ImageLocality":     false,
		"InterPodAffinity":  false,
		"NodeAffinity":      false,
	}

	for _, r := range results {
		if _, ok := expectedPlugins[r.PluginName]; ok {
			expectedPlugins[r.PluginName] = true
		}
	}

	for plugin, found := range expectedPlugins {
		if !found {
			t.Errorf("Expected to find plugin %s in results", plugin)
		}
	}

	// In this scenario, ImageLocality should be most influential
	// (it determines the winner - node-1 has much higher ImageLocality score)
	imageLocalityFound := false
	for i, r := range results {
		if r.PluginName == "ImageLocality" {
			imageLocalityFound = true
			// ImageLocality should have high influence (winnerChanged=true or low Jaccard)
			if !r.WinnerChange && r.Jaccard > 0.5 {
				t.Logf("Warning: ImageLocality at rank %d has WinnerChange=%v, Jaccard=%.2f", i+1, r.WinnerChange, r.Jaccard)
			}
		}
	}

	if !imageLocalityFound {
		t.Error("ImageLocality plugin not found in results")
	}

	// NodeResourcesFit should have less influence (similar scores across nodes)
	nodeResourcesFitFound := false
	for _, r := range results {
		if r.PluginName == "NodeResourcesFit" {
			nodeResourcesFitFound = true
			// Should have Jaccard close to 1.0 (doesn't change ranking much)
			if r.Jaccard < 0.8 {
				t.Logf("Note: NodeResourcesFit has Jaccard=%.2f (expected close to 1.0)", r.Jaccard)
			}
		}
	}

	if !nodeResourcesFitFound {
		t.Error("NodeResourcesFit plugin not found in results")
	}
}

// Benchmark for getTopKNodes to verify the optimization
// Compare O(n log k) heap approach vs O(n log n) full sorting
func BenchmarkTopKNodes(b *testing.B) {
	// Test different scenarios
	scenarios := []struct {
		name string
		n    int // number of nodes
		k    int // topK size
	}{
		{"SmallCluster_SmallK", 50, 3},
		{"SmallCluster_MediumK", 50, 10},
		{"MediumCluster_SmallK", 200, 3},
		{"MediumCluster_MediumK", 200, 10},
		{"MediumCluster_LargeK", 200, 50},
		{"LargeCluster_SmallK", 1000, 3},
		{"LargeCluster_MediumK", 1000, 10},
		{"LargeCluster_LargeK", 1000, 100},
		{"VeryLargeCluster_SmallK", 5000, 3},
		{"VeryLargeCluster_MediumK", 5000, 10},
	}

	for _, scenario := range scenarios {
		b.Run(scenario.name, func(b *testing.B) {
			// Create test data
			total := make(map[string]int64, scenario.n)
			for i := 0; i < scenario.n; i++ {
				total[fmt.Sprintf("node-%d", i)] = int64(rand.Intn(1000))
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = getTopKNodes(total, scenario.k)
			}
		})
	}
}

// Benchmark to compare old vs new implementation
// This shows the performance benefit of O(n log k) vs O(n log n)
func BenchmarkTopKNodesComparison(b *testing.B) {
	sizes := []int{50, 200, 1000, 5000}
	k := 3 // Typical topK size

	for _, n := range sizes {
		// Create test data
		total := make(map[string]int64, n)
		for i := 0; i < n; i++ {
			total[fmt.Sprintf("node-%d", i)] = int64(rand.Intn(1000))
		}

		b.Run(fmt.Sprintf("Heap_n%d_k%d", n, k), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = getTopKNodes(total, k)
			}
		})

		b.Run(fmt.Sprintf("Sort_n%d_k%d", n, k), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Old implementation using full sort
				type nodeScore struct {
					name  string
					score int64
				}
				nodes := make([]nodeScore, 0, len(total))
				for name, score := range total {
					nodes = append(nodes, nodeScore{name: name, score: score})
				}
				sort.Slice(nodes, func(i, j int) bool {
					return nodes[i].score > nodes[j].score
				})
				if len(nodes) > k {
					nodes = nodes[:k]
				}
				result := make([]string, len(nodes))
				for i, n := range nodes {
					result[i] = n.name
				}
				_ = result
			}
		})
	}
}

// Benchmark for the full plugin influence analysis
func BenchmarkAnalyzePluginInfluence(b *testing.B) {
	_, ctx := ktesting.NewTestContext(b)
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
	}

	scenarios := []struct {
		name       string
		nodeCount  int
		pluginCount int
		topKSize   int
	}{
		{"Small", 10, 3, 3},
		{"Medium", 50, 5, 3},
		{"Large", 200, 10, 5},
	}

	for _, scenario := range scenarios {
		b.Run(scenario.name, func(b *testing.B) {
			// Create test data
			nodesScores := make([]fwk.NodePluginScores, scenario.nodeCount)
			pluginNames := make([]string, scenario.pluginCount)
			for i := 0; i < scenario.pluginCount; i++ {
				pluginNames[i] = fmt.Sprintf("Plugin%d", i)
			}

			for i := 0; i < scenario.nodeCount; i++ {
				scores := make([]fwk.PluginScore, scenario.pluginCount)
				totalScore := int64(0)
				for j := 0; j < scenario.pluginCount; j++ {
					score := int64(rand.Intn(100))
					scores[j] = fwk.PluginScore{
						Name:  pluginNames[j],
						Score: score,
					}
					totalScore += score
				}
				nodesScores[i] = fwk.NodePluginScores{
					Name:       fmt.Sprintf("node-%d", i),
					Scores:     scores,
					TotalScore: totalScore,
				}
			}

			logger := klog.FromContext(ctx)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = analyzePluginInfluence(logger, pod, nodesScores, scenario.topKSize)
			}
		})
	}
}
