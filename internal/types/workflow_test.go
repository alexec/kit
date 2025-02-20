package types

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/yaml"
)

func TestPod(t *testing.T) {
	data, err := os.ReadFile("testdata/tasks.yaml")
	assert.NoError(t, err)
	wf := &Workflow{}
	err = yaml.Unmarshal(data, wf)
	assert.NoError(t, err)
	assert.Len(t, wf.Tasks, 2)
	task := wf.Tasks["foo"]
	assert.Equal(t, []uint16{8080}, task.GetHostPorts())
	assert.Equal(t, "Always", task.GetRestartPolicy())
	probe := task.GetReadinessProbe()
	assert.Equal(t, &Probe{TCPSocket: &TCPSocketAction{Port: 8080}}, probe)
	assert.Equal(t, 5*time.Second, probe.GetPeriod())
	assert.Equal(t, 5*time.Second, probe.GetInitialDelay())
	assert.Equal(t, 1, probe.GetSuccessThreshold())
	assert.Equal(t, 20, probe.GetFailureThreshold())
	assert.Nil(t, task.GetLivenessProbe())
	//
	tasks := wf.Tasks["bar"]
	assert.Equal(t, Strings{"sh", "-c", "echo bar"}, tasks.GetCommand())
	assert.Equal(t, Strings{"baz", "qux"}, tasks.Dependencies)
}

func TestPorts_Map(t *testing.T) {
	ports := Ports{
		{ContainerPort: 8080},
		{ContainerPort: 8081, HostPort: 80},
	}
	assert.Equal(t, map[uint16]uint16{8080: 8080, 8081: 80}, ports.Map())
}
