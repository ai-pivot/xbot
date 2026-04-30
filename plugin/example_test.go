package plugin

import (
	"context"
	"fmt"
)

func ExampleToolFromFunc() {
	tool := ToolFromFunc("greet", "Greets a person by name", func(ctx context.Context, input string) (string, error) {
		return "hello world", nil
	})
	fmt.Println(tool.Definition().Name)
	// Output: greet
}

func ExampleQuickManifest() {
	m := QuickManifest("com.example.myplugin", "My Plugin", "1.0.0", "Does cool things",
		WithPermissions("tools.register", "hooks.subscribe"),
	)
	fmt.Println(m.ID, m.Name, m.Version)
	// Output: com.example.myplugin My Plugin 1.0.0
}

func ExampleDenyHook() {
	handler := DenyHook("this tool is not allowed")
	result, _ := handler(context.Background(), &HookPayload{Event: HookPreToolUse})
	fmt.Println(result.Decision, result.Message)
	// Output: deny this tool is not allowed
}

func ExampleStaticEnricher() {
	enricher := StaticEnricher("Current timezone: UTC")
	content, _ := enricher(context.Background())
	fmt.Println(content)
	// Output: Current timezone: UTC
}
