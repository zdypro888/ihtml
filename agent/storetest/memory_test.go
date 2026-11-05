package storetest

import (
	"github.com/zdypro888/ihtml/agent"
	"testing"
)

func TestMemoryConformance(t *testing.T) {
	Run(t, func(t *testing.T) agent.TaskConfig {
		return agent.TaskConfig{Store: agent.NewMemoryTaskStore(), Results: agent.NewMemoryTaskResultStore(), ControlStore: agent.NewMemoryTaskControlStore(), Contexts: agent.NewMemoryTaskContextStore(), Events: agent.NewMemoryTaskEventStore(), Observations: agent.NewMemoryTaskObservationStore()}
	})
}
