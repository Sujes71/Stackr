package docker

import (
	"testing"

	"github.com/stackr/stackr/internal/config"
)

func TestDefaultQueueToolImage(t *testing.T) {
	tests := []struct {
		name      string
		queueType string
		want      string
	}{
		{
			name:      "kafka returns confluent image",
			queueType: "kafka",
			want:      "confluentinc/cp-kafka:latest",
		},
		{
			name:      "rabbitmq returns rabbitmq management image",
			queueType: "rabbitmq",
			want:      "rabbitmq:3-management",
		},
		{
			name:      "activemq returns activemq classic image",
			queueType: "activemq",
			want:      "apache/activemq-classic:latest",
		},
		{
			name:      "empty string returns empty",
			queueType: "",
			want:      "",
		},
		{
			name:      "unknown type returns empty",
			queueType: "unknown",
			want:      "",
		},
		{
			name:      "random string returns empty",
			queueType: "redis",
			want:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DefaultQueueToolImage(tt.queueType)
			if got != tt.want {
				t.Errorf("DefaultQueueToolImage(%q) = %q, want %q", tt.queueType, got, tt.want)
			}
		})
	}
}

func TestQueueToolImage(t *testing.T) {
	tests := []struct {
		name              string
		qse               *config.QueueSyncEnv
		want              string
	}{
		{
			name: "with override returns override image",
			qse: &config.QueueSyncEnv{
				QueueType: "kafka",
				ToolImage: "my-custom-kafka:2.0",
			},
			want: "my-custom-kafka:2.0",
		},
		{
			name: "without override returns default for kafka",
			qse: &config.QueueSyncEnv{
				QueueType: "kafka",
				ToolImage: "",
			},
			want: "confluentinc/cp-kafka:latest",
		},
		{
			name: "without override returns default for rabbitmq",
			qse: &config.QueueSyncEnv{
				QueueType: "rabbitmq",
				ToolImage: "",
			},
			want: "rabbitmq:3-management",
		},
		{
			name: "without override returns default for activemq",
			qse: &config.QueueSyncEnv{
				QueueType: "activemq",
				ToolImage: "",
			},
			want: "apache/activemq-classic:latest",
		},
		{
			name: "override takes priority even for unknown queue type",
			qse: &config.QueueSyncEnv{
				QueueType: "unknown-type",
				ToolImage: "my-special-image:latest",
			},
			want: "my-special-image:latest",
		},
		{
			name: "unknown type without override returns empty",
			qse: &config.QueueSyncEnv{
				QueueType: "nonexistent",
				ToolImage: "",
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := QueueToolImage(tt.qse)
			if got != tt.want {
				t.Errorf("QueueToolImage() = %q, want %q", got, tt.want)
			}
		})
	}
}
