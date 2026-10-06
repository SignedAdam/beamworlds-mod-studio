package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSettingsSizingDefaultsAndLegacyFallback(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)

	defaults, err := service.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if defaults.InterfaceSize != "default" || defaults.TextSize != "default" || defaults.ScrollbarColor != "#f26522" {
		t.Fatalf("sizing defaults = %q/%q, want default/default", defaults.InterfaceSize, defaults.TextSize)
	}

	legacySettings := `{"theme":"light","agentProfile":"chatgpt","contextMode":"balanced","emphasisColor":"#123456"}`
	if err := service.store.writeSetting(context.Background(), preferencesKey, legacySettings); err != nil {
		t.Fatal(err)
	}
	loaded, err := service.store.loadAppSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.InterfaceSize != "default" || loaded.TextSize != "default" {
		t.Fatalf("legacy sizing defaults = %q/%q, want default/default", loaded.InterfaceSize, loaded.TextSize)
	}
	if loaded.Theme != "light" || loaded.EmphasisColor != "#123456" || loaded.ScrollbarColor != "#f26522" {
		t.Fatalf("legacy appearance settings changed: %#v", loaded)
	}

	mixedSettings := `{"theme":"light","agentProfile":"chatgpt","contextMode":"balanced","interfaceSize":"  COMFORTABLE ","textSize":"unsupported","emphasisColor":"#654321","scrollbarColor":"not-a-color"}`
	if err := service.store.writeSetting(context.Background(), preferencesKey, mixedSettings); err != nil {
		t.Fatal(err)
	}
	loaded, err = service.store.loadAppSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.InterfaceSize != "comfortable" || loaded.TextSize != "default" {
		t.Fatalf("independent sizing fallback = %q/%q, want comfortable/default", loaded.InterfaceSize, loaded.TextSize)
	}
	if loaded.EmphasisColor != "#654321" || loaded.ScrollbarColor != "#f26522" {
		t.Fatalf("sizing fallback changed appearance colors: %q/%q", loaded.EmphasisColor, loaded.ScrollbarColor)
	}
}

func TestSettingsSizingValidationAllowsOnlyContractValues(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"compact", "default", "comfortable", "large"} {
		update := sizingSettingsUpdate()
		update.InterfaceSize = " " + strings.ToUpper(value) + " "
		validated, err := validateSettings(update)
		if err != nil {
			t.Fatalf("interface size %q rejected: %v", value, err)
		}
		if validated.InterfaceSize != value {
			t.Errorf("interface size %q normalized to %q", value, validated.InterfaceSize)
		}
	}
	for _, value := range []string{"small", "default", "large", "extra-large"} {
		update := sizingSettingsUpdate()
		update.TextSize = " " + strings.ToUpper(value) + " "
		validated, err := validateSettings(update)
		if err != nil {
			t.Fatalf("text size %q rejected: %v", value, err)
		}
		if validated.TextSize != value {
			t.Errorf("text size %q normalized to %q", value, validated.TextSize)
		}
	}

	for _, test := range []struct {
		name  string
		field func(*SettingsUpdate)
		want  string
	}{
		{name: "interface size", field: func(update *SettingsUpdate) { update.InterfaceSize = "huge" }, want: "interface size"},
		{name: "text size", field: func(update *SettingsUpdate) { update.TextSize = "giant" }, want: "text size"},
	} {
		update := sizingSettingsUpdate()
		test.field(&update)
		if _, err := validateSettings(update); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s invalid value error = %v, want an error mentioning %q", test.name, err, test.want)
		}
	}

	validated, err := validateSettings(sizingSettingsUpdate())
	if err != nil {
		t.Fatal(err)
	}
	if validated.InterfaceSize != "default" || validated.TextSize != "default" {
		t.Fatalf("empty sizing values = %q/%q, want default/default", validated.InterfaceSize, validated.TextSize)
	}
	invalidColor := sizingSettingsUpdate()
	invalidColor.ScrollbarColor = "not-a-color"
	validated, err = validateSettings(invalidColor)
	if err != nil {
		t.Fatal(err)
	}
	if validated.ScrollbarColor != "#f26522" {
		t.Fatalf("invalid scrollbar color = %q, want #f26522", validated.ScrollbarColor)
	}
	emptyColor := sizingSettingsUpdate()
	emptyColor.ScrollbarColor = ""
	validated, err = validateSettings(emptyColor)
	if err != nil {
		t.Fatal(err)
	}
	if validated.ScrollbarColor != "#f26522" {
		t.Fatalf("empty scrollbar color = %q, want #f26522", validated.ScrollbarColor)
	}
}

func TestLegacyScrollbarColorMigratesToEmphasisOrange(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)

	stored := `{"theme":"dark","agentProfile":"chatgpt","contextMode":"balanced","scrollbarColor":"#3F93C5"}`
	if err := service.store.writeSetting(context.Background(), preferencesKey, stored); err != nil {
		t.Fatal(err)
	}
	loaded, err := service.store.loadAppSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ScrollbarColor != "#f26522" {
		t.Fatalf("legacy scrollbar color = %q, want #f26522", loaded.ScrollbarColor)
	}

	reloaded, err := service.store.loadAppSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ScrollbarColor != "#f26522" {
		t.Fatalf("migration was not persisted: %q", reloaded.ScrollbarColor)
	}

	chosen := loaded
	chosen.ScrollbarColor = legacyScrollbarColor
	if err := service.store.saveAppSettings(context.Background(), chosen); err != nil {
		t.Fatal(err)
	}
	kept, err := service.store.loadAppSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if kept.ScrollbarColor != legacyScrollbarColor {
		t.Fatalf("deliberately chosen scrollbar color = %q, want %q", kept.ScrollbarColor, legacyScrollbarColor)
	}
}

func TestSettingsSizingPersistsAcrossReopenAndResetPreservesAppearance(t *testing.T) {
	t.Parallel()
	service := newTestAppService(t)
	update := sizingSettingsUpdate()
	update.Theme = "light"
	update.InterfaceSize = "comfortable"
	update.TextSize = "extra-large"

	saved, err := service.SaveSettings(update)
	if err != nil {
		t.Fatal(err)
	}
	if saved.InterfaceSize != "comfortable" || saved.TextSize != "extra-large" || saved.ScrollbarColor != "#789abc" {
		t.Fatalf("saved sizing = %q/%q/%q, want comfortable/extra-large/#789abc", saved.InterfaceSize, saved.TextSize, saved.ScrollbarColor)
	}
	encoded, err := service.store.readSetting(context.Background(), preferencesKey)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]json.RawMessage
	if err := json.Unmarshal([]byte(encoded), &persisted); err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]string{"interfaceSize": "comfortable", "textSize": "extra-large", "scrollbarColor": "#789abc"} {
		var got string
		if err := json.Unmarshal(persisted[field], &got); err != nil {
			t.Fatalf("persisted %s: %v", field, err)
		}
		if got != want {
			t.Errorf("persisted %s = %q, want %q", field, got, want)
		}
	}

	reopened, err := OpenStore(service.config.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, err := reopened.loadAppSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.InterfaceSize != "comfortable" || loaded.TextSize != "extra-large" {
		t.Fatalf("reopened sizing = %q/%q, want comfortable/extra-large", loaded.InterfaceSize, loaded.TextSize)
	}
	if loaded.Theme != "light" || loaded.EmphasisColor != "#123456" || loaded.ActiveTabColor != "#234567" || loaded.DarkTextColor != "#eeeeee" || loaded.LightTextColor != "#111111" || loaded.ScrollbarColor != "#789abc" {
		t.Fatalf("reopened appearance settings changed: %#v", loaded)
	}

	update.InterfaceSize = "default"
	update.TextSize = "default"
	reset, err := service.SaveSettings(update)
	if err != nil {
		t.Fatal(err)
	}
	if reset.InterfaceSize != "default" || reset.TextSize != "default" {
		t.Fatalf("reset sizing = %q/%q, want default/default", reset.InterfaceSize, reset.TextSize)
	}
	if reset.Theme != "light" || reset.EmphasisColor != "#123456" || reset.ActiveTabColor != "#234567" || reset.DarkTextColor != "#eeeeee" || reset.LightTextColor != "#111111" || reset.ScrollbarColor != "#789abc" {
		t.Fatalf("reset sizing changed unrelated appearance settings: %#v", reset)
	}
}

func sizingSettingsUpdate() SettingsUpdate {
	return SettingsUpdate{
		Theme:                "dark",
		AgentProfile:         "chatgpt",
		ContextMode:          "balanced",
		ShowAIUsage:          true,
		ShowFileSizes:        true,
		EmphasisColor:        "#123456",
		ActiveTabColor:       "#234567",
		SubsectionTitleColor: "#345678",
		DarkSurfaceColor:     "#456789",
		DarkBorderColor:      "#56789a",
		DarkTextColor:        "#eeeeee",
		LightSurfaceColor:    "#abcdef",
		LightBorderColor:     "#bcdefa",
		LightTextColor:       "#111111",
		ScrollbarColor:       "#789abc",
		PreScanReasoning:     "medium",
		FullScanReasoning:    "xhigh",
	}
}
