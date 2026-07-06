package config

import (
	"testing"
)

func TestService_QueueType(t *testing.T) {
	tests := []struct {
		name     string
		image    string
		wantType string
	}{
		// Kafka — direct keyword match
		{
			name:     "kafka keyword lowercase",
			image:    "apache/kafka:latest",
			wantType: "kafka",
		},
		{
			name:     "kafka keyword mixed case",
			image:    "bitnami/KAFKA",
			wantType: "kafka",
		},
		// Kafka — confluent keyword match
		{
			name:     "confluent keyword",
			image:    "confluentinc/cp-kafka:latest",
			wantType: "kafka",
		},
		{
			name:     "confluent keyword mixed case",
			image:    "Confluent/cp-server",
			wantType: "kafka",
		},
		// RabbitMQ
		{
			name:     "rabbitmq keyword lowercase",
			image:    "rabbitmq:3-management",
			wantType: "rabbitmq",
		},
		{
			name:     "rabbitmq keyword mixed case",
			image:    "bitnami/RabbitMQ",
			wantType: "rabbitmq",
		},
		// ActiveMQ
		{
			name:     "activemq keyword lowercase",
			image:    "apache/activemq-classic:latest",
			wantType: "activemq",
		},
		{
			name:     "activemq keyword mixed case",
			image:    "Apache/ActiveMQ-Classic",
			wantType: "activemq",
		},
		// Non-queue services
		{
			name:     "nginx should return empty",
			image:    "nginx:latest",
			wantType: "",
		},
		{
			name:     "postgres should return empty",
			image:    "postgres:16",
			wantType: "",
		},
		{
			name:     "empty image should return empty",
			image:    "",
			wantType: "",
		},
		{
			name:     "random image should return empty",
			image:    "redis:7-alpine",
			wantType: "",
		},
		// Edge cases
		{
			name:     "kafka substring in larger word",
			image:    "notkafkahere:latest",
			wantType: "kafka",
		},
		{
			name:     "rabbitmq substring in larger word",
			image:    "notrabbitmqhere:latest",
			wantType: "rabbitmq",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Service{Image: tt.image}
			got := s.QueueType()
			if got != tt.wantType {
				t.Errorf("QueueType() = %q, want %q", got, tt.wantType)
			}
		})
	}
}

func TestService_FindQueueSyncEnv(t *testing.T) {
	envA := QueueSyncEnv{Name: "staging", QueueType: "kafka", Host: "kafka.staging.com", Port: "9092"}
	envB := QueueSyncEnv{Name: "production", QueueType: "rabbitmq", Host: "mq.prod.com", Port: "5672"}

	tests := []struct {
		name       string
		envs       []QueueSyncEnv
		searchName string
		wantNil    bool
		wantName   string
	}{
		{
			name:       "find existing env by name",
			envs:       []QueueSyncEnv{envA, envB},
			searchName: "staging",
			wantNil:    false,
			wantName:   "staging",
		},
		{
			name:       "find second env by name",
			envs:       []QueueSyncEnv{envA, envB},
			searchName: "production",
			wantNil:    false,
			wantName:   "production",
		},
		{
			name:       "not found returns nil",
			envs:       []QueueSyncEnv{envA, envB},
			searchName: "nonexistent",
			wantNil:    true,
		},
		{
			name:       "empty slice returns nil",
			envs:       nil,
			searchName: "anything",
			wantNil:    true,
		},
		{
			name:       "nil slice returns nil",
			envs:       []QueueSyncEnv{},
			searchName: "anything",
			wantNil:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Service{QueueSyncEnvs: tt.envs}
			got := s.FindQueueSyncEnv(tt.searchName)

			if tt.wantNil {
				if got != nil {
					t.Errorf("FindQueueSyncEnv(%q) = %v, want nil", tt.searchName, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("FindQueueSyncEnv(%q) = nil, want non-nil", tt.searchName)
			}
			if got.Name != tt.wantName {
				t.Errorf("FindQueueSyncEnv(%q).Name = %q, want %q", tt.searchName, got.Name, tt.wantName)
			}
		})
	}
}

func TestQueueSyncEnv_Validate(t *testing.T) {
	tests := []struct {
		name       string
		qse        QueueSyncEnv
		wantErrors []string // field names that should have errors
	}{
		// Valid configs
		{
			name: "valid kafka env",
			qse: QueueSyncEnv{
				Name:      "staging",
				QueueType: "kafka",
				Host:      "kafka.staging.com",
				Port:      "9092",
			},
			wantErrors: nil,
		},
		{
			name: "valid rabbitmq env",
			qse: QueueSyncEnv{
				Name:      "prod-mq",
				QueueType: "rabbitmq",
				Host:      "mq.prod.com",
				Port:      "5672",
				User:      "admin",
			},
			wantErrors: nil,
		},
		{
			name: "valid activemq env",
			qse: QueueSyncEnv{
				Name:      "amq",
				QueueType: "activemq",
				Host:      "amq.internal",
				Port:      "61616",
			},
			wantErrors: nil,
		},
		{
			name: "empty port is ok (optional)",
			qse: QueueSyncEnv{
				Name:      "local-only",
				QueueType: "kafka",
				Host:      "localhost",
				Port:      "",
			},
			wantErrors: nil,
		},
		{
			name: "valid with tool image override",
			qse: QueueSyncEnv{
				Name:      "custom-image",
				QueueType: "rabbitmq",
				Host:      "10.0.0.1",
				Port:      "15672",
				ToolImage: "my-custom-rabbitmq:latest",
			},
			wantErrors: nil,
		},
		// Missing required fields
		{
			name: "missing name",
			qse: QueueSyncEnv{
				Name:      "   ",
				QueueType: "kafka",
				Host:      "kafka.local",
				Port:      "9092",
			},
			wantErrors: []string{"name"},
		},
		{
			name: "empty name",
			qse: QueueSyncEnv{
				Name:      "",
				QueueType: "kafka",
				Host:      "kafka.local",
				Port:      "9092",
			},
			wantErrors: []string{"name"},
		},
		{
			name: "missing host",
			qse: QueueSyncEnv{
				Name:      "test",
				QueueType: "kafka",
				Host:      "",
				Port:      "9092",
			},
			wantErrors: []string{"host"},
		},
		{
			name: "whitespace-only host",
			qse: QueueSyncEnv{
				Name:      "test",
				QueueType: "kafka",
				Host:      "   ",
				Port:      "9092",
			},
			wantErrors: []string{"host"},
		},
		// Invalid QueueType
		{
			name: "invalid queue type",
			qse: QueueSyncEnv{
				Name:      "test",
				QueueType: "invalidtype",
				Host:      "localhost",
				Port:      "9092",
			},
			wantErrors: []string{"queue_type"},
		},
		{
			name: "empty queue type",
			qse: QueueSyncEnv{
				Name:      "test",
				QueueType: "",
				Host:      "localhost",
				Port:      "9092",
			},
			wantErrors: []string{"queue_type"},
		},
		// Invalid ports
		{
			name: "port zero",
			qse: QueueSyncEnv{
				Name:      "test",
				QueueType: "kafka",
				Host:      "localhost",
				Port:      "0",
			},
			wantErrors: []string{"port"},
		},
		{
			name: "port over max",
			qse: QueueSyncEnv{
				Name:      "test",
				QueueType: "kafka",
				Host:      "localhost",
				Port:      "65536",
			},
			wantErrors: []string{"port"},
		},
		{
			name: "port non-numeric",
			qse: QueueSyncEnv{
				Name:      "test",
				QueueType: "kafka",
				Host:      "localhost",
				Port:      "abc",
			},
			wantErrors: []string{"port"},
		},
		{
			name: "negative port",
			qse: QueueSyncEnv{
				Name:      "test",
				QueueType: "kafka",
				Host:      "localhost",
				Port:      "-1",
			},
			wantErrors: []string{"port"},
		},
		// Multiple errors
		{
			name: "multiple errors: missing name, host, and invalid type",
			qse: QueueSyncEnv{
				Name:      "",
				QueueType: "unknown",
				Host:      "",
				Port:      "9092",
			},
			wantErrors: []string{"queue_type", "name", "host"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.qse.Validate()

			if len(tt.wantErrors) == 0 {
				if len(errs) != 0 {
					t.Errorf("Validate() returned unexpected errors: %v", errs)
				}
				return
			}

			for _, field := range tt.wantErrors {
				if _, ok := errs[field]; !ok {
					t.Errorf("Validate() missing expected error for field %q. Got errors: %v", field, errs)
				}
			}
		})
	}
}

func TestQueueSyncEnv_Validate_ErrorMessages(t *testing.T) {
	// Verify specific error messages
	t.Run("name error message", func(t *testing.T) {
		qse := QueueSyncEnv{QueueType: "kafka", Host: "localhost", Port: "9092", Name: ""}
		errs := qse.Validate()
		if msg, ok := errs["name"]; !ok || msg != "Name is required" {
			t.Errorf("name error = %q, want %q", msg, "Name is required")
		}
	})

	t.Run("host error message", func(t *testing.T) {
		qse := QueueSyncEnv{QueueType: "kafka", Host: "", Port: "9092", Name: "test"}
		errs := qse.Validate()
		if msg, ok := errs["host"]; !ok || msg != "Host is required" {
			t.Errorf("host error = %q, want %q", msg, "Host is required")
		}
	})

	t.Run("port error message", func(t *testing.T) {
		qse := QueueSyncEnv{QueueType: "kafka", Host: "localhost", Port: "0", Name: "test"}
		errs := qse.Validate()
		if msg, ok := errs["port"]; !ok || msg != "Port must be a number between 1 and 65535" {
			t.Errorf("port error = %q, want %q", msg, "Port must be a number between 1 and 65535")
		}
	})

	t.Run("queue type error message", func(t *testing.T) {
		qse := QueueSyncEnv{QueueType: "invalid", Host: "localhost", Port: "9092", Name: "test"}
		errs := qse.Validate()
		if msg, ok := errs["queue_type"]; !ok {
			t.Errorf("expected queue_type error, got none")
		} else if msg == "" {
			t.Error("queue_type error message is empty")
		}
	})
}

func TestQueueSyncEnv_Validate_ValidPorts(t *testing.T) {
	// Boundary test for valid ports
	validPorts := []string{"1", "80", "443", "9092", "5672", "61616", "65535"}
	for _, port := range validPorts {
		t.Run("port_"+port, func(t *testing.T) {
			qse := QueueSyncEnv{
				Name:      "test",
				QueueType: "kafka",
				Host:      "localhost",
				Port:      port,
			}
			errs := qse.Validate()
			if _, ok := errs["port"]; ok {
				t.Errorf("port %q should be valid, but got error: %v", port, errs["port"])
			}
		})
	}
}
