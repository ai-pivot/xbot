package agent

// Multimodal (vision) factory wiring tests — Phase 6 of the vision design.
//
// Vision is a PURELY MANUAL per-model switch (PerModelConfig.Vision, set in
// the model editor — NO built-in model-name whitelist). The factory reads it
// via resolveModelConfig (subscription_models.vision), assembles the
// MultimodalConfig via buildMultimodalConfig (nil = vision off), and threads
// the process-wide ImageResolver (SetImageResolver — serverapp wires the OSS
// + view_images resolver at boot) into every vision-enabled client.

import (
	"context"
	"testing"

	"xbot/llm"
	"xbot/storage/sqlite"
)

// fakeImageResolver is a minimal llm.ImageResolver for wiring tests.
type fakeImageResolver struct{}

func (fakeImageResolver) ResolveImage(ctx context.Context, ref string) (string, error) {
	return "data:image/png;base64,FAKE", nil
}

func TestBuildMultimodalConfig_VisionSwitch(t *testing.T) {
	f, _, _ := newModelFirstTestFactory(t)

	// Vision OFF (or absent) → nil MultimodalConfig (zero value: degrade
	// placeholders — non-vision models never receive image parts).
	if mm := f.buildMultimodalConfig(modelPerModelConfig{}); mm != nil {
		t.Fatalf("vision off must produce nil MultimodalConfig, got %+v", mm)
	}
	if mm := f.buildMultimodalConfig(modelPerModelConfig{present: true}); mm != nil {
		t.Fatalf("vision-off present row must produce nil, got %+v", mm)
	}

	// Vision ON without a resolver → enabled config, nil resolver (only
	// inline data: URLs resolve — degrade semantics).
	mm := f.buildMultimodalConfig(modelPerModelConfig{present: true, vision: true, visionDetail: "low"})
	if mm == nil || !mm.VisionEnabled || mm.VisionDetail != "low" {
		t.Fatalf("vision on without resolver: %+v", mm)
	}
	if mm.ImageResolver != nil {
		t.Fatalf("no resolver wired — expected nil ImageResolver, got %T", mm.ImageResolver)
	}

	// Vision ON with the factory resolver → the resolver threads through.
	f.SetImageResolver(fakeImageResolver{})
	mm = f.buildMultimodalConfig(modelPerModelConfig{present: true, vision: true, visionDetail: "high"})
	if mm == nil || !mm.VisionEnabled || mm.VisionDetail != "high" {
		t.Fatalf("vision on with resolver: %+v", mm)
	}
	if _, ok := mm.ImageResolver.(fakeImageResolver); !ok {
		t.Fatalf("factory resolver must thread into MultimodalConfig, got %T", mm.ImageResolver)
	}

	// MaxImages default is applied by the llm layer (parseMultimodalContent),
	// not here — the config carries the raw switch only.
	if mm.MaxImages != 0 {
		t.Fatalf("factory must not override the image budget (llm default), got %d", mm.MaxImages)
	}
}

func TestResolveModelConfig_ReadsVisionSwitch(t *testing.T) {
	f, subSvc, _ := newModelFirstTestFactory(t)

	sub := &sqlite.LLMSubscription{
		ID: "vis-factory-sub", SenderID: "cli_user", Name: "VisSub", Provider: "openai",
		BaseURL: "https://api.vis.example/v1", APIKey: "sk-vis", Model: "glm-4.6v",
	}
	if err := subSvc.Add(sub); err != nil {
		t.Fatalf("Add sub: %v", err)
	}
	if err := subSvc.UpsertModel(sub.ID, "glm-4.6v", 0, 0, "", ""); err != nil {
		t.Fatalf("UpsertModel: %v", err)
	}

	// Default: vision off (manual switch unset).
	pmc := f.resolveModelConfig(sub.ID, "glm-4.6v")
	if pmc.present && pmc.vision {
		t.Fatalf("new model must default to vision off, got %+v", pmc)
	}

	// Flip the manual switch → resolveModelConfig reads it back.
	if err := subSvc.SetModelVisionConfig(sub.ID, "glm-4.6v", true, "low"); err != nil {
		t.Fatalf("SetModelVisionConfig: %v", err)
	}
	pmc = f.resolveModelConfig(sub.ID, "glm-4.6v")
	if !pmc.present || !pmc.vision || pmc.visionDetail != "low" {
		t.Fatalf("resolveModelConfig must read the vision switch, got %+v", pmc)
	}

	// The assembled MultimodalConfig carries it.
	if mm := f.buildMultimodalConfig(pmc); mm == nil || !mm.VisionEnabled || mm.VisionDetail != "low" {
		t.Fatalf("buildMultimodalConfig from pmc: %+v", mm)
	}

	// Token-config writes never reset the switch (UpsertModel keeps vision).
	if err := subSvc.UpsertModel(sub.ID, "glm-4.6v", 128000, 8192, "", ""); err != nil {
		t.Fatalf("UpsertModel: %v", err)
	}
	pmc = f.resolveModelConfig(sub.ID, "glm-4.6v")
	if !pmc.vision || pmc.visionDetail != "low" {
		t.Fatalf("UpsertModel clobbered vision: %+v", pmc)
	}

	// Disable again → nil config (degrade placeholders).
	if err := subSvc.SetModelVisionConfig(sub.ID, "glm-4.6v", false, ""); err != nil {
		t.Fatalf("SetModelVisionConfig(disable): %v", err)
	}
	if mm := f.buildMultimodalConfig(f.resolveModelConfig(sub.ID, "glm-4.6v")); mm != nil {
		t.Fatalf("vision off after disable must be nil, got %+v", mm)
	}
}

// TestGetOrCreateClient_VisionSeparatesCacheEntries is the regression for
// "多模态模型没收到图片": the client cache key MUST include the per-model
// vision switch. With a (subID, apiType)-only key, a client built for a
// vision-OFF model was reused for a vision-ON model of the same subscription
// (and vice versa) — the model kept seeing the text placeholder
// "当前模型未开启视觉输入" even though subscription_models.vision was 1.
func TestGetOrCreateClient_VisionSeparatesCacheEntries(t *testing.T) {
	f, subSvc, _ := newModelFirstTestFactory(t)
	sub := &sqlite.LLMSubscription{
		ID: "vis-cache-sub", SenderID: "cli_user", Name: "VisCache", Provider: "openai",
		BaseURL: "https://api.vis.example/v1", APIKey: "sk-vis", Model: "text-model",
	}
	if err := subSvc.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := subSvc.UpsertModel(sub.ID, "text-model", 0, 0, "", ""); err != nil {
		t.Fatalf("UpsertModel text-model: %v", err)
	}
	if err := subSvc.UpsertModel(sub.ID, "vision-model", 0, 0, "", ""); err != nil {
		t.Fatalf("UpsertModel vision-model: %v", err)
	}
	if err := subSvc.SetModelVisionConfig(sub.ID, "vision-model", true, "low"); err != nil {
		t.Fatalf("SetModelVisionConfig: %v", err)
	}
	got, err := subSvc.Get(sub.ID)
	if err != nil || got == nil {
		t.Fatalf("Get sub: err=%v", err)
	}

	cText := f.getOrCreateClient(got, "text-model")
	cVision := f.getOrCreateClient(got, "vision-model")
	if cText == nil || cVision == nil {
		t.Fatalf("clients must build: text=%v vision=%v", cText, cVision)
	}
	if cText == cVision {
		t.Fatal("vision-off and vision-on models must NOT share a cached client (cache key must include the vision switch)")
	}
	// Same model again → cache hit (identical client instance).
	if again := f.getOrCreateClient(got, "text-model"); again != cText {
		t.Fatal("same model must reuse its cached client")
	}
	if again := f.getOrCreateClient(got, "vision-model"); again != cVision {
		t.Fatal("same vision model must reuse its cached client")
	}

	// Toggling the switch invalidates the subscription cache → the next build
	// reflects the new state (a distinct client from the old one).
	f.InvalidateSubscription(sub.ID)
	cVision2 := f.getOrCreateClient(got, "vision-model")
	if cVision2 == cVision {
		t.Fatal("after InvalidateSubscription the client must be rebuilt")
	}
}

// TestSetImageResolver_GetImageResolverRoundTrip verifies the factory-wide
// resolver setter/getter (serverapp wires the OSS resolver once at boot).
func TestSetImageResolver_GetImageResolverRoundTrip(t *testing.T) {
	f, _, _ := newModelFirstTestFactory(t)
	if f.getImageResolver() != nil {
		t.Fatal("resolver must default to nil")
	}
	f.SetImageResolver(fakeImageResolver{})
	if _, ok := f.getImageResolver().(fakeImageResolver); !ok {
		t.Fatalf("getImageResolver must return the wired resolver, got %T", f.getImageResolver())
	}
}

var _ = llm.MultimodalConfig{} // keep the llm import for the config type reference
