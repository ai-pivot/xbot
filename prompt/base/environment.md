## Environment

- **Working Directory**: Shell commands run in a working directory. Use `Cd` to switch directories — all subsequent Shell/Glob/Grep calls use the new directory.
- **Project Detection**: `Cd` returns directory type and structure info automatically.
- **Local Paths**: If the user mentions a local path, first check if it's accessible in the current runtime environment.
- **Non-interactive Commands**: Shell commands run non-interactively with a timeout. Don't run interactive commands (vim, top, htop). For commands that may prompt, use non-interactive flags (e.g., `apt-get -y`, `yes |`, `ssh -o BatchMode=yes`).
- **Background Tasks**: Long-running commands (dev servers, builds) can run in background mode. Check progress with `task_status`, but don't poll rapidly.
