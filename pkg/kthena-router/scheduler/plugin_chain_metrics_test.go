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

package scheduler

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	"github.com/volcano-sh/kthena/pkg/kthena-router/datastore"
	"github.com/volcano-sh/kthena/pkg/kthena-router/metrics"
)

func gaugeValue(t *testing.T, vec *prometheus.GaugeVec, lvs ...string) float64 {
	t.Helper()
	g, err := vec.GetMetricWithLabelValues(lvs...)
	require.NoError(t, err)
	m := &dto.Metric{}
	require.NoError(t, g.Write(m))
	return m.GetGauge().GetValue()
}

func counterValue(t *testing.T, vec *prometheus.CounterVec, lvs ...string) float64 {
	t.Helper()
	c, err := vec.GetMetricWithLabelValues(lvs...)
	require.NoError(t, err)
	m := &dto.Metric{}
	require.NoError(t, c.Write(m))
	return m.GetCounter().GetValue()
}

// TestProfileChainManager_Metrics drives the same event sequence as the status
// tests and checks that every SchedulerProfile metric follows it.
func TestProfileChainManager_Metrics(t *testing.T) {
	store := datastore.New()
	m := NewProfileChainManager(store)
	const profileName = "metrics-p1"
	const modelName = "ms"
	key := types.NamespacedName{Namespace: "default", Name: profileName}
	label := key.String()

	// Lazy build: the profile builds once a ModelServer references it, and a
	// successful build reports it as resolved. The gauge is written after
	// buildChain returns, outside the manager lock, so poll it rather than
	// BuildStatus.
	ms := newModelServerWithProfileRef("default", modelName, profileName)
	store.AddOrUpdateModelServer(ms, nil)
	store.AddOrUpdateSchedulerProfile(validProfile(profileName))
	waitUntil(t, 2*time.Second, func() bool {
		return gaugeValue(t, &metrics.DefaultMetrics.SchedulerProfileResolved, label) == 1
	}, "a successful build must report the profile as resolved")

	// Acquire and release track in-flight requests and count them per model.
	beforeReq := counterValue(t, &metrics.DefaultMetrics.SchedulerProfileRequestsTotal, label, modelName)
	chain, err := m.AcquireProfile(ms)
	require.NoError(t, err)
	require.NotNil(t, chain)
	assert.Equal(t, 1.0, gaugeValue(t, &metrics.DefaultMetrics.SchedulerProfileChainInflight, label),
		"an acquired chain must be counted as in-flight")
	assert.Equal(t, beforeReq+1,
		counterValue(t, &metrics.DefaultMetrics.SchedulerProfileRequestsTotal, label, modelName),
		"an acquired chain must count one request")
	chain.Release()
	assert.Equal(t, 0.0, gaugeValue(t, &metrics.DefaultMetrics.SchedulerProfileChainInflight, label),
		"a released chain must drain to zero")

	// A failed rebuild keeps the old chain serving but reports the profile as
	// unresolved and records the build error.
	beforeErr := counterValue(t, &metrics.DefaultMetrics.SchedulerProfileBuildErrorsTotal, label)
	store.AddOrUpdateSchedulerProfile(failedProfile(profileName))
	waitUntil(t, 2*time.Second, func() bool {
		return gaugeValue(t, &metrics.DefaultMetrics.SchedulerProfileResolved, label) == 0
	}, "a failed rebuild must report the profile as unresolved")
	assert.Equal(t, beforeErr+1,
		counterValue(t, &metrics.DefaultMetrics.SchedulerProfileBuildErrorsTotal, label),
		"a failed rebuild must record one build error")

	// Deleting the profile retires the chain and drops the instantaneous gauges.
	beforeRetired := counterValue(t, &metrics.DefaultMetrics.SchedulerProfileChainRetiredTotal, label)
	store.DeleteSchedulerProfile(key)
	waitUntil(t, 2*time.Second, func() bool {
		built, buildErr := m.BuildStatus(key)
		return !built && buildErr == nil
	}, "delete must remove the chain")
	assert.Equal(t, beforeRetired+1,
		counterValue(t, &metrics.DefaultMetrics.SchedulerProfileChainRetiredTotal, label),
		"deleting the profile must retire one chain")
	assert.Equal(t, 0.0, gaugeValue(t, &metrics.DefaultMetrics.SchedulerProfileResolved, label),
		"a deleted profile must not keep a resolved series")
}

// TestProfileChainManager_GlobalChainHasNoProfileMetrics makes sure the global
// chain, which has no profile, never creates a series with an empty label.
func TestProfileChainManager_GlobalChainHasNoProfileMetrics(t *testing.T) {
	store := datastore.New()
	m := NewProfileChainManager(store)

	// A ModelServer without a profile reference serves on the global chain.
	ms := newModelServerWithProfileRef("default", "global-ms", "unused")
	ms.Spec.TrafficPolicy = nil
	chain, err := m.AcquireProfile(ms)
	require.NoError(t, err)
	assert.Nil(t, chain, "a ModelServer without a profile reference must not resolve a profile chain")
}
