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
	"math"
	"sort"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	fwk "k8s.io/kube-scheduler/framework"
)

// PluginInfluenceResult represents the quantified influence of a single scoring plugin.
// It uses a hierarchical metric system to explain how a plugin affects the decision.
type PluginInfluenceResult struct {
	PluginName    string
	WinnerChange  bool    // Decision Layer: Did removing this plugin change the winner?
	Jaccard       float64 // Structural Layer: Similarity of TopK set (0.0 to 1.0)
	RankShift     int     // Magnitude Layer: Cumulative shift in rankings
	ScoreVariance int64   // Potential Impact: The max-min score spread of this plugin
}

// pluginScoresMap is a helper type for plugin influence analysis.
// plugin -> node -> score
type pluginScoresMap map[string]map[string]int64

// buildPluginScoresMap builds a plugin scores map from NodePluginScores.
func buildPluginScoresMap(nodesScores []fwk.NodePluginScores) pluginScoresMap {
	result := make(pluginScoresMap)
	for _, nodeScore := range nodesScores {
		for _, pluginScore := range nodeScore.Scores {
			if result[pluginScore.Name] == nil {
				result[pluginScore.Name] = make(map[string]int64)
			}
			result[pluginScore.Name][nodeScore.Name] = pluginScore.Score
		}
	}
	return result
}

// analyzePluginInfluence performs a Counterfactual Ablation analysis using Leave-One-Out (LOO).
// It quantifies the real impact of each plugin while keeping the environment constant.
func analyzePluginInfluence(logger klog.Logger, pod *v1.Pod, nodesScores []fwk.NodePluginScores, topKSize int) []PluginInfluenceResult {
	if len(nodesScores) == 0 {
		return nil
	}

	// Phase 1: Data Preparation
	// Build baseline total scores and a matrix of per-plugin scores.
	baselineScores := make(map[string]int64, len(nodesScores))
	pluginScores := make(map[string]map[string]int64)

	for _, ns := range nodesScores {
		baselineScores[ns.Name] = ns.TotalScore
		for _, ps := range ns.Scores {
			if pluginScores[ps.Name] == nil {
				pluginScores[ps.Name] = make(map[string]int64)
			}
			pluginScores[ps.Name][ns.Name] = ps.Score
		}
	}

	// Get the baseline TopK and winner (the "actual" scheduling result).
	baselineTopK := getTopKNodes(baselineScores, topKSize)
	if len(baselineTopK) == 0 {
		return nil
	}
	baselineWinner := baselineTopK[0]

	// Phase 2: Dynamic Threshold Pruning
	// Optimization: If the winner's lead exceeds the maximum possible impact of any plugin,
	// the result is mathematically stable, and we can skip the expensive LOO loop.
	maxPossibleImpact := int64(0)
	for _, scores := range pluginScores {
		if v := calculateVariance(scores); v > maxPossibleImpact {
			maxPossibleImpact = v
		}
	}

	if len(baselineTopK) >= 2 {
		winnerGap := baselineScores[baselineWinner] - baselineScores[baselineTopK[1]]
		if winnerGap > maxPossibleImpact {
			logger.V(5).Info("Decision is stable: winner gap exceeds any single plugin's max impact", "gap", winnerGap)
			return []PluginInfluenceResult{{PluginName: "N/A", Jaccard: 1.0}}
		}
	}

	var results []PluginInfluenceResult

	// Phase 3: Counterfactual Ablation (LOO Loop)
	for pluginName, pScores := range pluginScores {
		// Optimization: Skip plugins with zero variance as they cannot affect relative ranking.
		if isZeroVariance(pScores) {
			continue
		}

		// Counterfactual Reasoning: "What would happen if this plugin was removed?"
		// We subtract the plugin's contribution from the baseline instead of recomputing from scratch.
		modifiedScores := make(map[string]int64, len(baselineScores))
		for node, score := range baselineScores {
			modifiedScores[node] = score - pScores[node]
		}

		// Extract new TopK under the counterfactual scenario.
		newTopK := getTopKNodes(modifiedScores, topKSize)
		if len(newTopK) == 0 {
			continue
		}

		// Phase 4: Calculate Hierarchical Metrics
		results = append(results, PluginInfluenceResult{
			PluginName:    pluginName,
			WinnerChange:  newTopK[0] != baselineWinner,
			Jaccard:       calculateJaccard(baselineTopK, newTopK),
			RankShift:     calculateRankShift(baselineTopK, newTopK),
			ScoreVariance: calculateVariance(pScores),
		})
	}

	// Phase 5: Result Ranking
	// Sort by influence severity: WinnerChange > Structural Change (Jaccard) > Magnitude (RankShift)
	sort.Slice(results, func(i, j int) bool {
		if results[i].WinnerChange != results[j].WinnerChange {
			return results[i].WinnerChange
		}
		if results[i].Jaccard != results[j].Jaccard {
			return results[i].Jaccard < results[j].Jaccard
		}
		return results[i].RankShift > results[j].RankShift
	})

	return results
}

// getTopKNodes extracts top K nodes sorted by score.
// Uses standard sort for simplicity; O(N log N) is acceptable for typical cluster sizes.
func getTopKNodes(scores map[string]int64, k int) []string {
	nodes := make([]string, 0, len(scores))
	for name := range scores {
		nodes = append(nodes, name)
	}

	sort.Slice(nodes, func(i, j int) bool {
		if scores[nodes[i]] != scores[nodes[j]] {
			return scores[nodes[i]] > scores[nodes[j]]
		}
		return nodes[i] < nodes[j] // Deterministic tie-breaking by name
	})

	if k > len(nodes) {
		k = len(nodes)
	}
	return nodes[:k]
}

// calculateVariance returns the range (Max - Min), representing the plugin's differentiating power.
func calculateVariance(scores map[string]int64) int64 {
	if len(scores) == 0 {
		return 0
	}
	min, max := int64(math.MaxInt64), int64(math.MinInt64)
	for _, s := range scores {
		if s < min {
			min = s
		}
		if s > max {
			max = s
		}
	}
	return max - min
}

// isZeroVariance returns true if the plugin grants identical scores to all nodes.
func isZeroVariance(scores map[string]int64) bool {
	if len(scores) <= 1 {
		return true
	}
	var first int64
	initialized := false
	for _, s := range scores {
		if !initialized {
			first = s
			initialized = true
		} else if s != first {
			return false
		}
	}
	return true
}

// calculateJaccard computes the set similarity between baseline and modified TopK lists.
func calculateJaccard(a, b []string) float64 {
	setA := make(map[string]bool)
	for _, s := range a {
		setA[s] = true
	}
	intersect := 0
	for _, s := range b {
		if setA[s] {
			intersect++
		}
	}
	union := len(a) + len(b) - intersect
	if union == 0 {
		return 1.0
	}
	return float64(intersect) / float64(union)
}

// calculateRankShift measures the total absolute displacement of nodes in the ranking.
func calculateRankShift(baseline, current []string) int {
	baselineRank := make(map[string]int)
	for i, n := range baseline {
		baselineRank[n] = i
	}

	shift := 0
	penalty := len(baseline) // Penalty for nodes that dropped out of TopK

	currentSet := make(map[string]int)
	for i, n := range current {
		currentSet[n] = i
	}

	for node, oldRank := range baselineRank {
		if newRank, ok := currentSet[node]; ok {
			diff := oldRank - newRank
			if diff < 0 {
				diff = -diff
			}
			shift += diff
		} else {
			shift += penalty
		}
	}
	return shift
}

// logPluginInfluenceRanking logs the plugin influence ranking at debug level.
func logPluginInfluenceRanking(logger klog.Logger, pod *v1.Pod, results []PluginInfluenceResult, topKSize int) {
	if !logger.V(4).Enabled() || len(results) == 0 {
		return
	}

	// Check if this is a stable winner case (early exit)
	if len(results) == 1 && results[0].PluginName == "N/A" {
		logger.V(4).Info("Plugin influence analysis: winner is stable",
			"pod", klog.KObj(pod),
			"reason", "winner gap too large for any plugin to change")
		return
	}

	logger.V(4).Info("Scheduler decision influenced by scoring plugins",
		"pod", klog.KObj(pod),
		"pluginsAnalyzed", len(results))

	if !logger.V(5).Enabled() {
		return
	}

	for i, r := range results {
		changeStr := "No"
		if r.WinnerChange {
			changeStr = "Yes"
		}
		logger.V(5).Info("Plugin influence ranking",
			"pod", klog.KObj(pod),
			"rank", i+1,
			"plugin", r.PluginName,
			"winnerChanged", changeStr,
			"jaccard", r.Jaccard,
			"rankShift", r.RankShift,
			"scoreVariance", r.ScoreVariance)
	}
}
