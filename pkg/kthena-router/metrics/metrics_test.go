/*
Copyright The Volcano Authors.

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

package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func histCount(t *testing.T, vec *prometheus.HistogramVec, lvs ...string) uint64 {
	t.Helper()
	obs, err := vec.GetMetricWithLabelValues(lvs...)
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues: %v", err)
	}
	m := &dto.Metric{}
	if err := obs.(prometheus.Metric).Write(m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return m.GetHistogram().GetSampleCount()
}

func histSum(t *testing.T, vec *prometheus.HistogramVec, lvs ...string) float64 {
	t.Helper()
	obs, err := vec.GetMetricWithLabelValues(lvs...)
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues: %v", err)
	}
	m := &dto.Metric{}
	if err := obs.(prometheus.Metric).Write(m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return m.GetHistogram().GetSampleSum()
}

// histBucket returns the cumulative count of the bucket whose upper bound equals le.
func histBucket(t *testing.T, vec *prometheus.HistogramVec, le float64, lvs ...string) uint64 {
	t.Helper()
	obs, err := vec.GetMetricWithLabelValues(lvs...)
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues: %v", err)
	}
	m := &dto.Metric{}
	if err := obs.(prometheus.Metric).Write(m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	for _, b := range m.GetHistogram().GetBucket() {
		if b.GetUpperBound() == le {
			return b.GetCumulativeCount()
		}
	}
	t.Fatalf("bucket le=%v not found", le)
	return 0
}

func counterVal(t *testing.T, vec *prometheus.CounterVec, lvs ...string) float64 {
	t.Helper()
	c, err := vec.GetMetricWithLabelValues(lvs...)
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues: %v", err)
	}
	m := &dto.Metric{}
	if err := c.Write(m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return m.GetCounter().GetValue()
}

func gaugeVal(t *testing.T, vec *prometheus.GaugeVec, lvs ...string) float64 {
	t.Helper()
	g, err := vec.GetMetricWithLabelValues(lvs...)
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues: %v", err)
	}
	m := &dto.Metric{}
	if err := g.Write(m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return m.GetGauge().GetValue()
}

func TestFairnessMetricsAggregateUserIdentity(t *testing.T) {
	m := DefaultMetrics
	const model = "metricstest-fairness-aggregate-user"

	m.IncFairnessQueueSize(model, "alice@example.com")
	m.IncFairnessQueueSize(model, "bob@example.com")
	m.RecordFairnessQueueDuration(model, "alice@example.com", time.Millisecond)
	m.RecordFairnessQueueDuration(model, "bob@example.com", 2*time.Millisecond)
	m.IncFairnessQueueCancelled(model, "alice@example.com")
	m.IncFairnessQueueDequeue(model, "bob@example.com")

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}

	wantFamilies := map[string]bool{
		"kthena_router_fairness_queue_size":             false,
		"kthena_router_fairness_queue_duration_seconds": false,
		"kthena_router_fairness_queue_cancelled_total":  false,
		"kthena_router_fairness_queue_dequeue_total":    false,
	}
	for _, family := range families {
		if _, ok := wantFamilies[family.GetName()]; !ok {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := make(map[string]string, len(metric.GetLabel()))
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if labels[LabelModel] != model {
				continue
			}
			if got := labels[LabelUserID]; got != FairnessAggregateUserID {
				t.Errorf("%s user_id = %q, want %q", family.GetName(), got, FairnessAggregateUserID)
			}
			wantFamilies[family.GetName()] = true
		}
	}

	for family, found := range wantFamilies {
		if !found {
			t.Errorf("did not find %s for model %q", family, model)
		}
	}
}

func TestPrefixCacheMatchRatio(t *testing.T) {
	m := DefaultMetrics
	const model = "metricstest-prefix-matchratio"

	countBefore := histCount(t, &m.PrefixCacheMatchRatio, model)
	sumBefore := histSum(t, &m.PrefixCacheMatchRatio, model)
	missBefore := histBucket(t, &m.PrefixCacheMatchRatio, 0, model)

	m.RecordPrefixCacheMatchRatio(model, 0.5)
	m.RecordPrefixCacheMatchRatio(model, 0) // miss

	if got := histCount(t, &m.PrefixCacheMatchRatio, model) - countBefore; got != 2 {
		t.Errorf("match_ratio sample count delta = %d, want 2", got)
	}
	if got := histSum(t, &m.PrefixCacheMatchRatio, model) - sumBefore; got != 0.5 {
		t.Errorf("match_ratio sample sum delta = %v, want 0.5", got)
	}
	// The le=0 bucket counts misses, so hit/miss is derivable from the histogram.
	if got := histBucket(t, &m.PrefixCacheMatchRatio, 0, model) - missBefore; got != 1 {
		t.Errorf("match_ratio le=0 (miss) delta = %d, want 1", got)
	}
}

func TestPrefixCacheEviction(t *testing.T) {
	m := DefaultMetrics
	const model = "metricstest-prefix-eviction"

	evBefore := counterVal(t, &m.PrefixCacheEvictionsTotal, model)
	m.RecordPrefixCacheEviction(model)
	if got := counterVal(t, &m.PrefixCacheEvictionsTotal, model) - evBefore; got != 1 {
		t.Errorf("evictions delta = %v, want 1", got)
	}
}

func TestPrefixCacheEntriesProvider(t *testing.T) {
	m := DefaultMetrics
	unregister := m.RegisterPrefixCacheEntriesProvider(func() float64 { return 42 })
	defer unregister()

	dm := &dto.Metric{}
	if err := m.PrefixCacheEntries.Write(dm); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := dm.GetGauge().GetValue(); got != 42 {
		t.Errorf("entries gauge = %v, want 42", got)
	}
}

func TestPrefixCacheEntriesProvidersSumAndUnregister(t *testing.T) {
	m := DefaultMetrics
	unregisterA := m.RegisterPrefixCacheEntriesProvider(func() float64 { return 10 })
	unregisterB := m.RegisterPrefixCacheEntriesProvider(func() float64 { return 32 })

	gauge := func() float64 {
		dm := &dto.Metric{}
		if err := m.PrefixCacheEntries.Write(dm); err != nil {
			t.Fatalf("Write: %v", err)
		}
		return dm.GetGauge().GetValue()
	}

	// Multiple live instances (one per profile chain) are summed.
	if got := gauge(); got != 42 {
		t.Errorf("entries gauge = %v, want 42 (10 + 32)", got)
	}

	// A closed chain's provider stops contributing without disturbing others.
	unregisterA()
	if got := gauge(); got != 32 {
		t.Errorf("entries gauge after unregister = %v, want 32", got)
	}

	unregisterB()
	if got := gauge(); got != 0 {
		t.Errorf("entries gauge with no providers = %v, want 0", got)
	}
}

func TestKVCacheMatchRatioAndError(t *testing.T) {
	m := DefaultMetrics
	const model = "metricstest-kv-matchratio"

	countBefore := histCount(t, &m.KVCacheMatchRatio, model)
	missBefore := histBucket(t, &m.KVCacheMatchRatio, 0, model)
	redisErrBefore := counterVal(t, &m.KVCacheErrorsTotal, model, StageRedis)

	m.RecordKVCacheMatchRatio(model, 1.0)
	m.RecordKVCacheMatchRatio(model, 0) // miss
	m.RecordKVCacheError(model, StageRedis)

	if got := histCount(t, &m.KVCacheMatchRatio, model) - countBefore; got != 2 {
		t.Errorf("kvcache match_ratio sample count delta = %d, want 2", got)
	}
	if got := histBucket(t, &m.KVCacheMatchRatio, 0, model) - missBefore; got != 1 {
		t.Errorf("kvcache match_ratio le=0 (miss) delta = %d, want 1", got)
	}
	if got := counterVal(t, &m.KVCacheErrorsTotal, model, StageRedis) - redisErrBefore; got != 1 {
		t.Errorf("kvcache redis errors delta = %v, want 1", got)
	}
}

func TestKVCacheDurations(t *testing.T) {
	m := DefaultMetrics
	const model = "metricstest-kv-durations"

	redisBefore := histCount(t, &m.KVCacheRedisDuration, model)
	tokBefore := histCount(t, &m.KVCacheTokenizeDuration, model)

	m.RecordKVCacheRedisDuration(model, 3*time.Millisecond)
	m.RecordKVCacheTokenizeDuration(model, 7*time.Millisecond)

	if got := histCount(t, &m.KVCacheRedisDuration, model) - redisBefore; got != 1 {
		t.Errorf("redis duration sample count delta = %d, want 1", got)
	}
	if got := histCount(t, &m.KVCacheTokenizeDuration, model) - tokBefore; got != 1 {
		t.Errorf("tokenize duration sample count delta = %d, want 1", got)
	}
}

func TestRequestRecorderDelegation(t *testing.T) {
	m := DefaultMetrics
	const model = "metricstest-recorder"
	r := NewRequestMetricsRecorder(m, model, "/v1/chat/completions")

	prefixBefore := histCount(t, &m.PrefixCacheMatchRatio, model)
	kvBefore := histCount(t, &m.KVCacheMatchRatio, model)

	r.RecordPrefixCacheMatchRatio(0.25)
	r.RecordKVCacheMatchRatio(0.75)

	if got := histCount(t, &m.PrefixCacheMatchRatio, model) - prefixBefore; got != 1 {
		t.Errorf("recorder prefix match_ratio delta = %d, want 1", got)
	}
	if got := histCount(t, &m.KVCacheMatchRatio, model) - kvBefore; got != 1 {
		t.Errorf("recorder kvcache match_ratio delta = %d, want 1", got)
	}
}

func TestRequestRecorderBindsDestinationLabels(t *testing.T) {
	m := DefaultMetrics
	const model = "metricstest-recorder-destination"
	const path = "/v1/chat/completions"
	const modelRoute = "default/mr-external"
	const backendName = "default/openai-provider"
	const upstreamModel = "gpt-4o-mini"
	destination := DestinationLabels{
		ModelRoute:    modelRoute,
		BackendType:   BackendTypeExternalProvider,
		BackendName:   backendName,
		UpstreamModel: upstreamModel,
	}
	r := NewRequestMetricsRecorder(m, model, path)

	inputBefore := counterVal(t, &m.TokensTotal, model, path, TokenTypeInput, modelRoute, BackendTypeExternalProvider, backendName, upstreamModel)
	outputBefore := counterVal(t, &m.TokensTotal, model, path, TokenTypeOutput, modelRoute, BackendTypeExternalProvider, backendName, upstreamModel)
	requestBefore := counterVal(t, &m.RequestsTotal, model, path, "200", "successful_request", modelRoute, BackendTypeExternalProvider, backendName, upstreamModel)
	durationBefore := histCount(t, &m.RequestDuration, model, path, "200", modelRoute, BackendTypeExternalProvider, backendName, upstreamModel)

	r.RecordInputTokens(5)
	if got := counterVal(t, &m.TokensTotal, model, path, TokenTypeInput, modelRoute, BackendTypeExternalProvider, backendName, upstreamModel) - inputBefore; got != 0 {
		t.Errorf("input token delta before destination bind = %v, want 0", got)
	}

	r.BindDestination(destination)
	r.RecordOutputTokens(3)
	r.Finish("200", "successful_request")

	if got := counterVal(t, &m.TokensTotal, model, path, TokenTypeInput, modelRoute, BackendTypeExternalProvider, backendName, upstreamModel) - inputBefore; got != 5 {
		t.Errorf("input token delta after destination bind = %v, want 5", got)
	}
	if got := counterVal(t, &m.TokensTotal, model, path, TokenTypeOutput, modelRoute, BackendTypeExternalProvider, backendName, upstreamModel) - outputBefore; got != 3 {
		t.Errorf("output token delta = %v, want 3", got)
	}
	if got := counterVal(t, &m.RequestsTotal, model, path, "200", "successful_request", modelRoute, BackendTypeExternalProvider, backendName, upstreamModel) - requestBefore; got != 1 {
		t.Errorf("request delta = %v, want 1", got)
	}
	if got := histCount(t, &m.RequestDuration, model, path, "200", modelRoute, BackendTypeExternalProvider, backendName, upstreamModel) - durationBefore; got != 1 {
		t.Errorf("request duration sample count delta = %d, want 1", got)
	}
}

func TestRequestRecorderFlushesInputTokensAsUnresolved(t *testing.T) {
	m := DefaultMetrics
	const model = "metricstest-recorder-unresolved"
	const path = "/v1/chat/completions"
	r := NewRequestMetricsRecorder(m, model, path)

	before := counterVal(t, &m.TokensTotal, model, path, TokenTypeInput, DestinationLabelValueNone, BackendTypeUnresolved, DestinationLabelValueNone, DestinationLabelValueNone)
	r.RecordInputTokens(7)
	r.Finish("404", "model_server_matching")

	if got := counterVal(t, &m.TokensTotal, model, path, TokenTypeInput, DestinationLabelValueNone, BackendTypeUnresolved, DestinationLabelValueNone, DestinationLabelValueNone) - before; got != 7 {
		t.Errorf("unresolved input token delta = %v, want 7", got)
	}
}

func TestRequestRecorderAttributesTokensToThePathSelectedDuringRouting(t *testing.T) {
	m := DefaultMetrics
	const model = "metricstest-recorder-selected-path"
	const selectedPath = "/custom"
	r := NewRequestMetricsRecorder(m, model, UnknownPath)

	selectedBefore := counterVal(t, &m.TokensTotal, model, selectedPath, TokenTypeInput, DestinationLabelValueNone, BackendTypeUnresolved, DestinationLabelValueNone, DestinationLabelValueNone)
	fallbackBefore := counterVal(t, &m.TokensTotal, model, UnknownPath, TokenTypeInput, DestinationLabelValueNone, BackendTypeUnresolved, DestinationLabelValueNone, DestinationLabelValueNone)

	// Input tokens are measured before routing, so the path selected while
	// routing must also apply to them.
	r.RecordInputTokens(5)
	r.SetPathLabel(selectedPath)
	r.BindDestination(DestinationLabels{})

	if got := counterVal(t, &m.TokensTotal, model, selectedPath, TokenTypeInput, DestinationLabelValueNone, BackendTypeUnresolved, DestinationLabelValueNone, DestinationLabelValueNone) - selectedBefore; got != 5 {
		t.Errorf("input token delta on the selected path = %v, want 5", got)
	}
	if got := counterVal(t, &m.TokensTotal, model, UnknownPath, TokenTypeInput, DestinationLabelValueNone, BackendTypeUnresolved, DestinationLabelValueNone, DestinationLabelValueNone) - fallbackBefore; got != 0 {
		t.Errorf("input token delta on the fallback path = %v, want 0", got)
	}
}

func TestActiveUpstreamRequestsKeepModelServerCompatibilityLabel(t *testing.T) {
	m := DefaultMetrics
	const modelRoute = "default/mr-external"
	const backendName = "default/openai-provider"
	const upstreamModel = "gpt-4o-mini"
	destination := DestinationLabels{
		ModelRoute:    modelRoute,
		BackendType:   BackendTypeExternalProvider,
		BackendName:   backendName,
		UpstreamModel: upstreamModel,
	}

	before := gaugeVal(t, &m.ActiveUpstreamRequests, DestinationLabelValueNone, modelRoute, BackendTypeExternalProvider, backendName, upstreamModel)
	m.IncActiveUpstreamRequestsForDestination(destination)
	if got := gaugeVal(t, &m.ActiveUpstreamRequests, DestinationLabelValueNone, modelRoute, BackendTypeExternalProvider, backendName, upstreamModel) - before; got != 1 {
		t.Errorf("external active upstream delta = %v, want 1", got)
	}
	m.DecActiveUpstreamRequestsForDestination(destination)
	if got := gaugeVal(t, &m.ActiveUpstreamRequests, DestinationLabelValueNone, modelRoute, BackendTypeExternalProvider, backendName, upstreamModel) - before; got != 0 {
		t.Errorf("external active upstream delta after decrement = %v, want 0", got)
	}
}

func TestSchedulerProfileMetrics(t *testing.T) {
	m := DefaultMetrics
	const profile = "default/metricstest-profile"

	// Resolution gauge reflects both states.
	m.SetSchedulerProfileResolved(profile, true)
	if got := gaugeVal(t, &m.SchedulerProfileResolved, profile); got != 1 {
		t.Errorf("resolved gauge = %v, want 1", got)
	}
	m.SetSchedulerProfileResolved(profile, false)
	if got := gaugeVal(t, &m.SchedulerProfileResolved, profile); got != 0 {
		t.Errorf("resolved gauge after failure = %v, want 0", got)
	}

	// Build duration is observed once per attempt.
	beforeCount := histCount(t, &m.SchedulerProfileBuildDuration, profile)
	beforeSum := histSum(t, &m.SchedulerProfileBuildDuration, profile)
	m.RecordSchedulerProfileBuildDuration(profile, 10*time.Millisecond)
	m.RecordSchedulerProfileBuildDuration(profile, 30*time.Millisecond)
	if got := histCount(t, &m.SchedulerProfileBuildDuration, profile) - beforeCount; got != 2 {
		t.Errorf("build duration sample count delta = %v, want 2", got)
	}
	if got := histSum(t, &m.SchedulerProfileBuildDuration, profile) - beforeSum; got < 0.039 || got > 0.041 {
		t.Errorf("build duration sum delta = %v, want ~0.04", got)
	}

	// Build errors accumulate per profile.
	beforeErr := counterVal(t, &m.SchedulerProfileBuildErrorsTotal, profile)
	m.IncSchedulerProfileBuildError(profile)
	m.IncSchedulerProfileBuildError(profile)
	if got := counterVal(t, &m.SchedulerProfileBuildErrorsTotal, profile) - beforeErr; got != 2 {
		t.Errorf("build error delta = %v, want 2", got)
	}

	// In-flight gauge tracks the current count, not a cumulative one.
	m.SetSchedulerProfileChainInflight(profile, 3)
	if got := gaugeVal(t, &m.SchedulerProfileChainInflight, profile); got != 3 {
		t.Errorf("chain inflight = %v, want 3", got)
	}
	m.SetSchedulerProfileChainInflight(profile, 0)
	if got := gaugeVal(t, &m.SchedulerProfileChainInflight, profile); got != 0 {
		t.Errorf("chain inflight after drain = %v, want 0", got)
	}

	// Retired counter and per-model request counter accumulate.
	beforeRetired := counterVal(t, &m.SchedulerProfileChainRetiredTotal, profile)
	m.IncSchedulerProfileChainRetired(profile)
	if got := counterVal(t, &m.SchedulerProfileChainRetiredTotal, profile) - beforeRetired; got != 1 {
		t.Errorf("retired delta = %v, want 1", got)
	}
	const model = "metrics-model"
	beforeReq := counterVal(t, &m.SchedulerProfileRequestsTotal, profile, model)
	m.IncSchedulerProfileRequest(profile, model)
	if got := counterVal(t, &m.SchedulerProfileRequestsTotal, profile, model) - beforeReq; got != 1 {
		t.Errorf("request delta = %v, want 1", got)
	}
}

func TestClearSchedulerProfileDropsGaugesKeepsCounters(t *testing.T) {
	m := DefaultMetrics
	const profile = "default/metricstest-cleared-profile"

	m.SetSchedulerProfileResolved(profile, true)
	m.SetSchedulerProfileChainInflight(profile, 5)
	m.IncSchedulerProfileBuildError(profile)

	m.ClearSchedulerProfile(profile)

	if got := gaugeVal(t, &m.SchedulerProfileResolved, profile); got != 0 {
		t.Errorf("resolved gauge after clear = %v, want 0", got)
	}
	if got := gaugeVal(t, &m.SchedulerProfileChainInflight, profile); got != 0 {
		t.Errorf("chain inflight after clear = %v, want 0", got)
	}
	// Cumulative counters describe what happened while the profile existed and
	// must survive the deletion.
	if got := counterVal(t, &m.SchedulerProfileBuildErrorsTotal, profile); got < 1 {
		t.Errorf("build error counter after clear = %v, want >= 1", got)
	}
}
