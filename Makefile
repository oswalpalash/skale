LOCALBIN ?= $(CURDIR)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
IMAGE ?= ghcr.io/oswalpalash/skale-controller:dev
TIMESFM_IMAGE ?= ghcr.io/oswalpalash/skale-timesfm-runner:dev
LDFLAGS ?= -X github.com/oswalpalash/skale/internal/version.Version=$(VERSION) -X github.com/oswalpalash/skale/internal/version.Commit=$(COMMIT) -X github.com/oswalpalash/skale/internal/version.BuildDate=$(BUILD_DATE)

.PHONY: build test test-ci lint generate manifests docker-build timesfm-docker-build demo-design-partner demo-live-hpa demo-live-hpa-learning kind-up kind-down kind-status

build:
	go build -ldflags "$(LDFLAGS)" ./...

test:
	go test ./...

test-ci:
	CGO_ENABLED=0 go test ./...
	CGO_ENABLED=0 go vet ./...

lint:
	go vet ./...

generate: $(CONTROLLER_GEN)
	$(CONTROLLER_GEN) object paths=./api/...

manifests: $(CONTROLLER_GEN)
	mkdir -p config/crd/bases
	$(CONTROLLER_GEN) crd:allowDangerousTypes=true paths=./api/... output:crd:artifacts:config=config/crd/bases

demo-design-partner:
	./hack/demo-design-partner.sh

demo-live-hpa:
	./hack/demo-live-hpa.sh

demo-live-hpa-learning:
	WORKLOAD_READINESS_DELAY_SECONDS=12 \
	POLICY_WARMUP_OVERRIDE=20s \
	POLICY_FORECAST_HORIZON_OVERRIDE=30s \
	POLICY_COOLDOWN_WINDOW_OVERRIDE=20s \
	HPA_SCALE_UP_STABILIZATION_SECONDS=0 \
	HPA_SCALE_DOWN_STABILIZATION_SECONDS=20 \
	STEP_SECONDS=10 \
	LOAD_SCHEDULE=1,1,1,6,6,6,1,1,1,1,1,1 \
	LOAD_REPEATS=18 \
	LOOKBACK_DURATION=30m \
	REPLAY_DURATION=7m \
	UI_FOCUS_DURATION=10m \
	FORECAST_HORIZON=30s \
	FORECAST_SEASONALITY=2m \
	WARMUP_DURATION=20s \
	COOLDOWN_WINDOW=20s \
	./hack/demo-live-hpa.sh

kind-up:
	./hack/kind-cluster.sh up

kind-down:
	./hack/kind-cluster.sh down

kind-status:
	./hack/kind-cluster.sh status

docker-build:
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(IMAGE) .

timesfm-docker-build:
	docker build -f Dockerfile.timesfm -t $(TIMESFM_IMAGE) .

$(CONTROLLER_GEN):
	mkdir -p $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.20.1
