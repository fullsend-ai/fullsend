package behaviourtest

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/cucumber/godog"
	messages "github.com/cucumber/messages/go/v34"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/suite"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

const (
	// maxProvisioningPerShard caps the provisioning scenario instances
	// one CI shard (one runner job) runs. The planner adds shards as the
	// suite grows, so no runner hosts more than this many fresh installs.
	maxProvisioningPerShard = 20

	// defaultShardConcurrency is the most scenarios a CI shard runs at
	// once when GODOG_CONCURRENCY is unset, however many orgs it holds.
	defaultShardConcurrency = 12

	envShardIndex = "BEHAVIOUR_SHARD_INDEX"
	envShardTotal = "BEHAVIOUR_SHARD_TOTAL"
	envPlanFile   = "BEHAVIOUR_SHARD_PLAN_FILE"
)

// shardSpec is the CI shard a run executes: Index of Total, zero-based.
type shardSpec struct {
	Index int
	Total int
}

// shardSpecFromEnv reads BEHAVIOUR_SHARD_INDEX and BEHAVIOUR_SHARD_TOTAL.
// ok is false when neither is set: the run executes every selected
// scenario (local runs, STAGE). Setting only one of them is an error.
func shardSpecFromEnv() (spec shardSpec, ok bool, err error) {
	rawIndex := strings.TrimSpace(os.Getenv(envShardIndex))
	rawTotal := strings.TrimSpace(os.Getenv(envShardTotal))
	if rawIndex == "" && rawTotal == "" {
		return shardSpec{}, false, nil
	}
	if rawIndex == "" || rawTotal == "" {
		return shardSpec{}, false, fmt.Errorf("%s and %s must be set together (got %q and %q)", envShardIndex, envShardTotal, rawIndex, rawTotal)
	}
	total, err := strconv.Atoi(rawTotal)
	if err != nil || total < 1 {
		return shardSpec{}, false, fmt.Errorf("%s must be a positive integer, got %q", envShardTotal, rawTotal)
	}
	index, err := strconv.Atoi(rawIndex)
	if err != nil || index < 0 || index >= total {
		return shardSpec{}, false, fmt.Errorf("%s must be an integer in [0, %d), got %q", envShardIndex, total, rawIndex)
	}
	return shardSpec{Index: index, Total: total}, true, nil
}

// pickleKey identifies a scenario instance by its source AST nodes and
// content. AST node IDs distinguish byte-identical Outline rows; the
// planner and godog use the same incrementing ID generator and feature
// path order, so each row gets the same key in both parses.
func pickleKey(p *messages.Pickle) string {
	h := sha256.New()
	write := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	write(p.Uri)
	write(p.Name)
	for _, id := range p.AstNodeIds {
		write("ast:" + id)
	}
	for _, tag := range p.Tags {
		if tag != nil {
			write("tag:" + tag.Name)
		}
	}
	for _, step := range p.Steps {
		if step == nil {
			continue
		}
		write("step:" + step.Text)
		writeStepArgument(write, step.Argument)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeStepArgument(write func(string), arg *messages.PickleStepArgument) {
	if arg == nil {
		return
	}
	if ds := arg.DocString; ds != nil {
		write("docstring:" + ds.MediaType)
		write(ds.Content)
	}
	if dt := arg.DataTable; dt != nil {
		for _, row := range dt.Rows {
			if row == nil {
				continue
			}
			write("row")
			for _, cell := range row.Cells {
				if cell != nil {
					write(cell.Value)
				}
			}
		}
	}
}

// plannedShard is one CI shard's share of the selected instances.
type plannedShard struct {
	Index     int
	Instances []scenarioInstance
	// Provisioning counts the instances that will run and lease a fresh
	// repository; Other counts the rest (non-provisioning instances and
	// those that compatibility tags skip).
	Provisioning int
	Other        int
}

func (s *plannedShard) total() int { return s.Provisioning + s.Other }

// instanceGroup is the instances that share one key.
type instanceGroup struct {
	key          string
	instances    []scenarioInstance
	provisioning bool
}

// planShards assigns every selected instance to exactly one shard.
// Provisioning instances (those that will run and lease a fresh repo,
// classified as summarizeDemand does) are spread so no shard has more
// than perShardCap; the shard count starts at the lower bound
// ceil(provisioning/perShardCap), and at least one, so non-provisioning
// instances still run when nothing provisions. Groups of identical
// instances are placed first, then single instances on the shard with the
// fewest provisioning instances; should a group still not fit, another
// shard is added rather than exceeding the cap. The greedy placement is
// not guaranteed to use the minimum number of shards. Distinct Outline
// rows have distinct source IDs even when their content is identical.
// A group with the same source key larger than perShardCap is an error.
// Non-provisioning instances then go to the shard with the fewest
// instances. The plan depends only on the feature paths and content,
// so every CI job computes the same plan from the same checkout, tag
// filter and capabilities.
func planShards(instances []scenarioInstance, skip func(tags []string) error, perShardCap int) ([]plannedShard, error) {
	if perShardCap < 1 {
		return nil, fmt.Errorf("per-shard provisioning cap must be positive, got %d", perShardCap)
	}
	var groups []*instanceGroup
	byKey := make(map[string]*instanceGroup)
	provisioningTotal := 0
	for _, inst := range instances {
		provisions := inst.Provisions && skip(inst.Tags) == nil
		g, ok := byKey[inst.Key]
		if !ok {
			g = &instanceGroup{key: inst.Key, provisioning: provisions}
			byKey[inst.Key] = g
			groups = append(groups, g)
		}
		g.instances = append(g.instances, inst)
		if provisions {
			provisioningTotal++
		}
	}

	count := max(1, (provisioningTotal+perShardCap-1)/perShardCap)
	shards := make([]*plannedShard, count)
	for i := range shards {
		shards[i] = &plannedShard{Index: i}
	}

	// Place larger groups (identical instances) first, so singletons fill
	// around them instead of forcing an extra shard.
	var provGroups []*instanceGroup
	for _, g := range groups {
		if g.provisioning {
			provGroups = append(provGroups, g)
		}
	}
	sort.SliceStable(provGroups, func(i, j int) bool {
		return len(provGroups[i].instances) > len(provGroups[j].instances)
	})
	for _, g := range provGroups {
		size := len(g.instances)
		if size > perShardCap {
			return nil, fmt.Errorf("%d identical provisioning instances of %q exceed the per-shard cap %d", size, g.instances[0].URI+": "+g.instances[0].Name, perShardCap)
		}
		var best *plannedShard
		for _, s := range shards {
			if s.Provisioning+size > perShardCap {
				continue
			}
			if best == nil || s.Provisioning < best.Provisioning {
				best = s
			}
		}
		if best == nil {
			best = &plannedShard{Index: len(shards)}
			shards = append(shards, best)
		}
		best.Instances = append(best.Instances, g.instances...)
		best.Provisioning += size
	}
	for _, g := range groups {
		if g.provisioning {
			continue
		}
		best := shards[0]
		for _, s := range shards[1:] {
			if s.total() < best.total() {
				best = s
			}
		}
		best.Instances = append(best.Instances, g.instances...)
		best.Other += len(g.instances)
	}

	out := make([]plannedShard, len(shards))
	for i, s := range shards {
		out[i] = *s
	}
	return out, nil
}

// SuiteShardPlan is the CI shard plan for a behaviour suite, as written
// to BEHAVIOUR_SHARD_PLAN_FILE for the workflow's dynamic matrix.
type SuiteShardPlan struct {
	// Total is the number of shards (CI jobs); always at least 1.
	Total int `json:"total"`
	// Shards lists the shard indexes 0..Total-1, for the matrix.
	Shards []int `json:"shards"`
	// Selected is the scenario instances the tag filter selected,
	// Skipped those compatibility tags skip, and Provisioning those
	// that will run and lease a fresh repository.
	Selected     int `json:"selected"`
	Skipped      int `json:"skipped"`
	Provisioning int `json:"provisioning"`
	// MaxProvisioningPerShard is the cap the plan was made with.
	MaxProvisioningPerShard int `json:"max_provisioning_per_shard"`
	// PerShard is each shard's assignment.
	PerShard []ShardAssignment `json:"per_shard"`
}

// ShardAssignment is one shard's share of a SuiteShardPlan.
type ShardAssignment struct {
	Index        int `json:"index"`
	Instances    int `json:"instances"`
	Provisioning int `json:"provisioning"`
	Other        int `json:"other"`
}

// suiteSkipFunc returns the compatibility-tag check the suite applies
// before routing a scenario, for the runner config in cfg.
func suiteSkipFunc(cfg env.RunnerConfig) func(tags []string) error {
	base := &world.World{Config: cfg}
	return func(tags []string) error { return suite.SkipErrorForTagNames(tags, base) }
}

// PlanSuiteShards computes the CI shard plan for opts with the same
// inputs RunSuite uses (GODOG_TAGS, BEHAVIOUR_* runner config,
// ENVIRONMENT). It needs no credentials. STAGE always gets one shard:
// its pool is a single org, so splitting it across runners would only
// serialize the shards on that org's lock.
func PlanSuiteShards(opts SuiteOptions) (SuiteShardPlan, error) {
	if err := opts.validate(); err != nil {
		return SuiteShardPlan{}, fmt.Errorf("invalid suite options: %w", err)
	}
	cfg := env.LoadRunnerConfig()
	if err := cfg.Validate(); err != nil {
		return SuiteShardPlan{}, fmt.Errorf("invalid behaviour runner config: %w", err)
	}
	instances, err := loadScenarioInstances(opts.FeaturePaths, os.Getenv("GODOG_TAGS"))
	if err != nil {
		return SuiteShardPlan{}, fmt.Errorf("reading feature files: %w", err)
	}
	skip := suiteSkipFunc(cfg)
	perShardCap := maxProvisioningPerShard
	if cfg.Environment == "stage" {
		perShardCap = max(len(instances), 1)
	}
	shards, err := planShards(instances, skip, perShardCap)
	if err != nil {
		return SuiteShardPlan{}, err
	}
	return describePlan(shards, summarizeDemand(instances, skip), perShardCap), nil
}

func describePlan(shards []plannedShard, demand suiteDemand, perShardCap int) SuiteShardPlan {
	plan := SuiteShardPlan{
		Total:                   len(shards),
		Selected:                demand.Selected,
		Skipped:                 demand.Skipped,
		Provisioning:            demand.Provisioning,
		MaxProvisioningPerShard: perShardCap,
	}
	for _, s := range shards {
		plan.Shards = append(plan.Shards, s.Index)
		plan.PerShard = append(plan.PerShard, ShardAssignment{
			Index:        s.Index,
			Instances:    len(s.Instances),
			Provisioning: s.Provisioning,
			Other:        s.Other,
		})
	}
	return plan
}

// WriteSuiteShardPlan computes the plan and, when path is non-empty,
// writes it there as JSON. It returns the plan for logging.
func WriteSuiteShardPlan(opts SuiteOptions, path string) (SuiteShardPlan, error) {
	plan, err := PlanSuiteShards(opts)
	if err != nil {
		return plan, err
	}
	if path == "" {
		return plan, nil
	}
	data, err := json.Marshal(plan)
	if err != nil {
		return plan, fmt.Errorf("encoding shard plan: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return plan, fmt.Errorf("writing shard plan: %w", err)
	}
	return plan, nil
}

// ShardPlanFileFromEnv returns BEHAVIOUR_SHARD_PLAN_FILE.
func ShardPlanFileFromEnv() string {
	return strings.TrimSpace(os.Getenv(envPlanFile))
}

// shardGate admits only the scenarios planned for one shard. Every
// selected scenario instance belongs to exactly one shard of the plan;
// the others are skipped here and run on their own shard. It is safe
// for concurrent use.
type shardGate struct {
	spec  shardSpec
	owner map[string]int
	want  map[string]int
	names map[string]string

	mu      sync.Mutex
	seen    map[string]int
	unknown []string
}

func newShardGate(spec shardSpec, shards []plannedShard) *shardGate {
	g := &shardGate{
		spec:  spec,
		owner: make(map[string]int),
		want:  make(map[string]int),
		names: make(map[string]string),
		seen:  make(map[string]int),
	}
	for _, s := range shards {
		for _, inst := range s.Instances {
			g.owner[inst.Key] = s.Index
			g.names[inst.Key] = inst.URI + ": " + inst.Name
			if s.Index == spec.Index {
				g.want[inst.Key]++
			}
		}
	}
	return g
}

// Gate is the suite.ScenarioGate for this shard: nil runs the scenario,
// godog.ErrSkip leaves it to the shard that owns it, and a scenario the
// plan does not know fails, since no shard would run it.
func (g *shardGate) Gate(sc *godog.Scenario) error {
	key := pickleKey(sc)
	g.mu.Lock()
	defer g.mu.Unlock()
	owner, ok := g.owner[key]
	if !ok {
		name := sc.Uri + ": " + sc.Name
		g.unknown = append(g.unknown, name)
		return fmt.Errorf("scenario %q is not in the shard plan; the plan and godog disagree about the selected scenarios", name)
	}
	if owner != g.spec.Index {
		return godog.ErrSkip
	}
	g.seen[key]++
	return nil
}

// Verify reports whether godog started exactly the instances planned
// for this shard: none missing, none extra, none unknown to the plan.
func (g *shardGate) Verify() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	var problems []string
	keys := make([]string, 0, len(g.want))
	for key := range g.want {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if got, want := g.seen[key], g.want[key]; got != want {
			problems = append(problems, fmt.Sprintf("%q started %d time(s), planned %d", g.names[key], got, want))
		}
	}
	for _, name := range g.unknown {
		problems = append(problems, fmt.Sprintf("%q is not in the plan", name))
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New("shard " + g.spec.String() + " did not run its planned scenarios exactly once: " + strings.Join(problems, "; "))
}

func (s shardSpec) String() string {
	return fmt.Sprintf("%d/%d", s.Index+1, s.Total)
}

// selectShard plans the shards for instances and returns the one spec
// names. It fails when the plan's shard count differs from spec.Total,
// which means this job and the plan job saw different scenarios (a
// different checkout, tag filter or capabilities): running anyway could
// drop or repeat scenarios.
func selectShard(spec shardSpec, instances []scenarioInstance, skip func(tags []string) error) (plannedShard, *shardGate, error) {
	shards, err := planShards(instances, skip, maxProvisioningPerShard)
	if err != nil {
		return plannedShard{}, nil, err
	}
	if len(shards) != spec.Total {
		return plannedShard{}, nil, fmt.Errorf("shard plan drift: %s=%d but these %d scenario instance(s) plan into %d shard(s); the plan job and this shard must use the same checkout, GODOG_TAGS and BEHAVIOUR_* config",
			envShardTotal, spec.Total, len(instances), len(shards))
	}
	return shards[spec.Index], newShardGate(spec, shards), nil
}
