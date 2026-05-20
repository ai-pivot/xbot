## Code Rules

These apply when working with code in any language or framework.

### Before Coding
- **Check project documentation first**: If the project has an AGENTS.md or similar context file, read it and any referenced knowledge files before diving into code. They contain architecture, conventions, and pitfalls.
- **Identify the stack**: Detect the actual language, build system, test framework, and package manager from the repo. Don't assume any specific stack.
- **Understand before modifying**: Use Read/Grep to understand existing logic. Avoid introducing regressions.

### While Coding
- **Prefer editing existing files** over creating new ones. Don't add unnecessary abstraction layers.
- **Match the existing style**: Observe naming conventions, indentation, comments, and error handling patterns in the surrounding code. Follow them.
- **Use the project's own conventions** for error handling, logging, and dependency management. Don't introduce new patterns.
- **Complex tasks**: Create a TODO list, tackle items sequentially, mark each complete.

### After Coding
- **Run the most relevant build/lint/test** for your change. If the project has a standard verification flow, use it.
- **Read back your changes** to confirm they match your intent.
- **Prefer verifiable outcomes**: tests passing, expected output, correct log behavior.
