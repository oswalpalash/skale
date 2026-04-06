# AGENTS.md

## Purpose

This repository builds a **burst-readiness controller for Kubernetes**.

The product is **not** a generic “AI autoscaler.”  
The product is **not** a broad cost-optimization platform.  
The product is **not** a replacement for Kubernetes autoscaling primitives.

The product exists to help teams answer one question safely:

> “Would this workload have benefited from predictive pre-scaling, and if so, when, by how much, and under what safety constraints?”

The first version of the system is **recommendation-first** and **replay-first**.  
Trust must precede automation.

---

## Product definition

### One-line definition

A Kubernetes-native system that analyzes workload metrics, forecasts short-horizon demand for suitable services, and produces **safe, explainable replica recommendations** with **historical replay** before any production actuation.

### What we are building

We are building a controller and supporting components that:

1. ingest workload and platform metrics
2. estimate near-term load for supported workload types
3. convert forecasts into bounded replica recommendations
4. explain every recommendation
5. replay historical behavior against a baseline
6. fail closed when telemetry quality or operating conditions are poor

### What we are not building in v1

Do not drift into any of the following:

- a generic Kubernetes optimization suite
- a broad FinOps platform
- a node autoscaler
- a KEDA replacement
- a black-box autonomous control plane
- an ML research project optimized for benchmark novelty
- a product that requires perfect observability to function at all

---

## Product principles

All agents working in this repository must preserve these principles.

### 1. Recommendation-first
Default mode is advisory.  
No production writes are assumed.  
Automation must be explicitly enabled later and only after replay-backed trust is earned.

### 2. Replay is first-class
Replay is not a side feature.  
Replay is a core product surface and a core moat.  
If a proposed change improves “real-time recommendations” but weakens replay fidelity or interpretability, reject it.

### 3. Safety over aggressiveness
We prefer missing an opportunity over causing instability.  
The controller must fail closed when uncertain.

### 4. Explainability is mandatory
Every recommendation must be inspectable.  
A human operator must be able to answer:
- what signal changed
- what model/path produced the recommendation
- what policy bounded it
- why it was suppressed if no recommendation was made

### 5. Narrow scope wins
v1 exists for a narrow workload class:
- HPA-managed Deployments
- bursty API-like workloads
- meaningful warmup lag
- recurring or semi-predictable demand patterns
- sufficient metrics quality

Anything outside this wedge must be treated as out of scope unless explicitly approved.

### 6. Kubernetes-native composability
We augment existing systems rather than replacing them wholesale.
Design should respect Kubernetes idioms:
- CRDs
- controller-runtime patterns
- status conditions
- auditability
- safe convergence loops

### 7. Operational realism
Pods are not always the bottleneck.
Agents must explicitly account for:
- node capacity lag
- telemetry gaps
- scheduling delays
- dependency bottlenecks
- deploy windows
- anomalous bursts

---

## Repository mission

The repository should enable a user to:

1. define a supported workload and policy using a CRD
2. ingest relevant metrics from Prometheus and cluster signals
3. compute short-horizon forecasts using simple, production-practical models
4. generate safe replica recommendations
5. replay historical behavior against a baseline
6. inspect output via CLI, CRD status, logs, and exported reports

---

## v1 scope

### Supported
Agents may implement and refine the following:

- recommendation-only mode
- one primary CRD for predictive scaling policy
- HPA-managed Deployments as first-class target
- Prometheus-based metrics ingestion
- simple forecasting approaches:
  - seasonal naive
  - Holt-Winters
  - simple ensemble / fallback selection
- warmup-aware replica calculation
- replay engine using historical metrics
- explainability schema for each recommendation
- telemetry readiness checks
- safety policy evaluation
- suppression reasons and failure reporting
- CLI/report outputs for design-partner-style analysis

### Explicitly out of scope
Agents must not spend meaningful effort on:

- full autonomous actuation by default
- advanced ML models as a headline feature
- LSTM / deep learning experiments
- broad multi-cluster fleet management
- generalized cost governance
- full node pre-provisioning orchestration
- broad queue / event / KEDA-first support
- GPU-specialized scheduling
- service mesh redesign
- replacing HPA, Karpenter, or Cluster Autoscaler

If a task touches these areas, prefer a small extension point or clear TODO rather than building the full system.

---

## Ideal user and buyer context

This repository serves platform teams and SREs who manage workloads such as:

- JVM microservices with slow startup
- API services with recurring peak windows
- internal shared services with diurnal load patterns
- other latency-sensitive, HPA-managed services with measurable warmup cost

Agents should optimize for the operator workflow of:
- platform engineer
- SRE
- infrastructure lead
- engineering manager evaluating replay results

Do not optimize first for a data scientist workflow.

---

## Architecture boundaries

The architecture should remain legible and modular.

### Core components

#### 1. API / CRD layer
Defines desired policy and target workload mapping.

#### 2. Metrics ingestion layer
Fetches and validates Prometheus and cluster-derived signals.

#### 3. Forecasting layer
Produces short-horizon forecasts with confidence signals and fallback logic.

#### 4. Recommendation engine
Transforms demand forecasts into bounded replica suggestions.

#### 5. Safety engine
Applies suppression logic, hard limits, guardrails, and node/headroom sanity checks.

#### 6. Replay engine
Simulates historical recommendations and compares them to a baseline.

#### 7. Explainability layer
Produces structured machine-readable and human-readable decision records.

#### 8. Output surfaces
CRD status, logs, CLI output, reports, and optional UI later.

### Non-goals for architecture
Avoid creating:
- a giant monolith with no separations between inference, policy, replay, and outputs
- deeply coupled model-specific logic everywhere
- an architecture that assumes a hosted SaaS from day one
- an architecture that cannot run locally for replay/report generation

---

## Golden path for development

When making implementation decisions, prefer the following order:

1. make replay work for one supported workload class
2. make outputs clear and trustworthy
3. make recommendations safe and bounded
4. improve model selection only when it improves decision quality measurably
5. consider automation only after recommendation and replay are strong

If a proposed change improves model sophistication but reduces simplicity, debuggability, or trust, reject it.

---

## Key design assumptions

Agents should build with these assumptions visible in code and docs.

### 1. Predictability is conditional
Not all burst behavior is forecastable.
v1 is best for seasonality-dominated or recurrent bursts.

### 2. Metrics quality is a gating factor
Telemetry readiness is not optional.
The system must detect insufficient data and say so plainly.

### 3. Node capacity can invalidate pod recommendations
Replica advice is only useful if the cluster can likely schedule and run the pods in time.

### 4. Human operators need evidence
Every output should help a skeptical operator reason about risk, value, and limitations.

### 5. Baselines matter
A recommendation without comparison to current behavior is incomplete.

---

## Safety rules

These are non-negotiable.

### Recommendation suppression
The system must support suppressing recommendations when:

- forecast confidence is too low
- telemetry completeness is below threshold
- model disagreement is too high
- recent forecast error exceeds a threshold
- known blackout window is active
- dependency health signals indicate likely downstream bottlenecks
- schedulable node headroom is clearly insufficient
- recent scaling activity implies the system has not yet stabilized
- deployment / rollout / incident windows are active

### Required safety controls
Every supported policy should be able to express:

- min replicas
- max replicas
- max step-up
- max step-down
- confidence threshold
- cooldown / stabilization window
- blackout windows
- anomaly suppression toggle / thresholds
- node headroom sanity mode
- fallback behavior

### Circuit breaker
If recent recommendation quality is poor, the system must fail closed and surface the reason.

### Explicit non-claim
Do not imply that predictive replica recommendations alone solve node provisioning delays.
This repository must not hide that limitation.

---

## Metrics contract

Agents must treat telemetry as a product surface.

### Minimum viable metrics
The product should clearly document and validate minimum requirements, including:

- request rate or equivalent demand signal
- current replica count
- pod readiness / startup timing or derivable proxy
- CPU and memory saturation signals
- optional latency and error signals
- optional node capacity / schedulable headroom signals

### Readiness validation
Before generating strong recommendations, the system should determine:

- retention window sufficiency
- missing data fraction
- scrape resolution adequacy
- signal volatility
- label consistency
- whether warmup lag is known or estimable

### Deliverable outputs
Agents should expose:
- telemetry readiness summary
- per-signal health status
- reasons a workload is unsupported or low-confidence

---

## Replay requirements

Replay is a primary product capability.

### Replay must answer
For a given workload and historical window:

- what the baseline did
- what the predictive recommendation would have suggested
- when the recommendation would have occurred
- expected overload minutes reduced
- expected excess headroom increased or reduced
- recommendation confidence / suppression reasons
- sensitivity under forecast error or configuration changes

### Replay baseline
The system should compare against at least:
- observed current replica behavior
- reconstructed or approximate HPA behavior where possible
- naive alternatives when needed

### Replay realism
Agents should model, at minimum where feasible:
- warmup lag
- recommendation lead time
- scheduling delay proxies
- cooldown windows
- bounded scale deltas

Do not present replay as exact truth.
Replay is evidence, not certainty.

### Replay outputs
Outputs should be machine-readable and human-readable.
Preferred forms:
- structured JSON
- concise CLI summary
- markdown report
- CRD status snippets where relevant

---

## Explainability contract

Every recommendation or suppression should produce a structured explanation.

### Required explanation fields
At minimum:

- workload identity
- timestamp / evaluation window
- source signals used
- forecast method used
- forecast horizon
- confidence score / confidence class
- baseline replica count
- recommended replica count
- policy bounds applied
- warmup assumption
- suppression reason(s), if any
- model fallback reason, if any
- node headroom check result
- telemetry readiness state

### Tone of explanation
Use precise, operational language.
Avoid hype.
Avoid anthropomorphic model descriptions.

Bad:
- “The AI decided traffic looked scary.”

Good:
- “SeasonalNaive forecasted 420 RPS over the next 4 minutes. With 45s warmup and target utilization 70%, the bounded recommendation was 12 replicas. Recommendation suppressed because schedulable node headroom was insufficient.”

---

## CRD guidance

The CRD is a core contract.
Agents should not make it noisy or overfit it to future fantasies.

### CRD should support
- targetRef to supported workload
- mode, defaulting to recommendationOnly
- forecast horizon
- warmup configuration
- confidence threshold
- min/max replicas
- safety policies
- blackout windows
- optional known events
- optional dependency checks
- status conditions and explanation pointers

### CRD should not become
- a dumping ground for every future optimization idea
- an entire policy DSL in v1
- tightly coupled to one forecasting model implementation

---

## Forecasting guidance

### Approved v1 forecasting approaches
Agents should prioritize simple, production-practical methods:

- seasonal naive
- Holt-Winters
- simple weighted or ranked fallback
- simple heuristics if they improve stability and are explainable

### Forecasting priorities
Optimize for:
1. explainability
2. robustness
3. low operational cost
4. sane fallback behavior
5. simple retraining / calibration requirements

Do not optimize for:
- benchmark novelty
- deep learning sophistication
- opaque feature engineering
- hidden online adaptation loops

### Model selection guidance
If multiple models are available, the system should support choosing the simplest reliable one and falling back when confidence is low or divergence is high.

---

## Node-coupling guidance

This repository must take node constraints seriously.

### Required behavior
Agents should incorporate at least a basic node headroom sanity check before surfacing strong scale-up recommendations.

### Acceptable v1 stance
It is acceptable for v1 to say:
- “Recommendation may be unschedulable under current cluster headroom”
- “Node provisioning is not handled automatically”
- “Use with sufficient spare capacity or complementary node scaling policies”

### Unacceptable v1 stance
Do not imply:
- guaranteed readiness
- solved node provisioning
- full-cluster capacity intelligence
when the system only produces pod-level recommendations

---

## KEDA guidance

KEDA is not the wedge for v1.

### Default posture
HPA-managed services are first-class.
KEDA integration is exploratory and secondary.

### Rule
Do not spend major engineering effort on KEDA integration unless:
- clear differentiated value is documented
- the integration contract is concrete
- replay comparison against KEDA baseline is defined

If uncertain, defer.

---

## Logging and observability standards

All major decision paths should be observable.

### Logs must support
- decision traceability
- failure diagnosis
- replay debugging
- model fallback tracing
- signal validation diagnosis

### Avoid
- noisy logs with no structure
- logs that omit workload identity
- logs that hide suppression reasons

### Preferred logging traits
- structured
- stable field names
- correlation IDs where useful
- concise but sufficient

---

## Testing standards

Agents must treat tests as part of product credibility.

### Required test categories

#### Unit tests
For:
- forecast calculations
- recommendation math
- policy bounding
- suppression logic
- explanation generation

#### Integration tests
For:
- CRD reconciliation
- Prometheus query integration contracts
- status updates
- end-to-end recommendation flow

#### Replay tests
For:
- historical window handling
- baseline comparisons
- sensitivity logic
- report generation

#### Failure-mode tests
Must explicitly cover:
- missing metrics
- broken labels
- low-confidence forecasts
- node headroom insufficiency
- blackout windows
- recent high forecast error
- deployment rollouts
- anomalous spikes

### Principle
A change that improves happy-path throughput but weakens failure-mode coverage is usually a bad change.

---

## Documentation standards

Agents should write documentation for skeptical operators, not hype-driven buyers.

### Docs must be clear about
- supported workload classes
- telemetry requirements
- known limitations
- safety model
- replay assumptions
- baseline caveats
- node-coupling limitations
- when not to use the product

### Docs must avoid
- inflated claims
- “self-driving infrastructure” language
- implying cost savings are guaranteed
- implying broad workload generality

---

## Output style guidelines

Whenever generating CLI or report text, use this tone:

- calm
- technical
- explicit
- auditable
- minimally opinionated

Prefer:
- “unsupported”
- “low confidence”
- “suppressed”
- “bounded”
- “estimated”
- “observed”
- “reconstructed”

Avoid:
- “smart”
- “autonomous”
- “magic”
- “revolutionary”
- “guaranteed”

---

## Prioritization rubric

When choosing between tasks, use this order:

### Tier 1
- replay fidelity
- recommendation correctness
- suppression safety
- explainability
- telemetry validation

### Tier 2
- CRD ergonomics
- CLI/report usability
- controller reliability
- forecast model fallback behavior

### Tier 3
- performance tuning
- advanced models
- optional integrations
- UI polish

### Tier 4
- broad automation
- multi-cluster enterprise features
- generalized optimization scope

If a Tier 4 task competes with Tier 1 or Tier 2 work, reject or defer it.

---

## Anti-drift rules

Agents must actively resist these drift patterns.

### Drift pattern 1: “Let’s just add full automation”
Reject unless recommendation quality, replay quality, and safety coverage are already strong.

### Drift pattern 2: “Let’s become a cost platform”
Reject. Cost may be a secondary output later, not the primary wedge.

### Drift pattern 3: “Let’s support every workload type”
Reject. Narrow wedge beats broad fragility.

### Drift pattern 4: “Let’s use a more powerful model”
Reject unless it improves real decisions and remains explainable and operationally sane.

### Drift pattern 5: “Let’s hide uncertainty from users”
Reject. Confidence and limitations must remain visible.

---

## Definition of done

A feature is only done when:

1. it is consistent with the product wedge
2. it does not broaden scope irresponsibly
3. it includes safety behavior
4. it includes explainability outputs
5. it is test-covered for failure modes
6. it is documented with limitations
7. it improves trust, evidence, or operational clarity

If those conditions are not met, the feature is not done.

---

## Agent roles

These are conceptual roles for coding agents and maintainers working on the repository.

### Product scope guardian
Protects the wedge.
Rejects scope drift.
Asks:
- does this help recommendation-first adoption?
- does this preserve narrow HPA-first scope?

### Controller engineer
Builds reconciliation logic, status handling, CRD behavior, and safe convergence loops.

### Metrics and telemetry engineer
Owns Prometheus query design, data validation, missingness handling, and signal health reporting.

### Forecasting engineer
Implements simple, robust forecasting methods and fallback logic.
Prioritizes legibility over novelty.

### Replay engineer
Builds historical simulation and baseline comparison.
Protects replay credibility.

### Safety engineer
Owns suppression logic, bounds, circuit breakers, and node headroom sanity checks.

### Explainability and UX engineer
Ensures outputs are readable, structured, and decision-useful.

### Docs and operator enablement engineer
Writes docs that help skeptical platform teams evaluate and deploy safely.

---

## First milestones

Agents should aim for the following practical sequence.

### Milestone 1
Static replay for one workload using recorded metrics.

### Milestone 2
CRD + controller that evaluates workload, validates telemetry, and writes recommendation status.

### Milestone 3
Explainability schema and CLI/report generation.

### Milestone 4
Safety engine with suppression and circuit breaker behavior.

### Milestone 5
Operator-quality docs, sample workloads, and benchmark harness.

Only after these are strong should broader integration work be considered.

---

## Final rule

Build the product that a skeptical VP of Infrastructure would trust enough to trial on one service.

Not the product that sounds most ambitious in a pitch.
