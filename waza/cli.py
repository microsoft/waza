"""CLI entrypoint for waza."""

from __future__ import annotations

import sys
from datetime import datetime
from pathlib import Path
from typing import Any

import click
from rich.console import Console
from rich.panel import Panel
from rich.prompt import Confirm, Prompt
from rich.table import Table

from waza import __version__
from waza.reporters import GitHubReporter, JSONReporter, MarkdownReporter
from waza.runner import EvalRunner
from waza.schemas.eval_spec import EvalSpec

console = Console()


@click.group()
@click.version_option(version=__version__, prog_name="waza")
def main():
    """waza - Evaluate Agent Skills with precision."""
    pass


@main.command()
@click.argument("eval_path", type=click.Path(exists=True))
@click.option("--task", "-t", multiple=True, help="Run specific task(s) by ID")
@click.option("--output", "-o", type=click.Path(), help="Output file path")
@click.option("--format", "-f", type=click.Choice(["json", "markdown", "github"]), default="json", help="Output format")
@click.option("--trials", type=int, help="Override trials per task")
@click.option("--parallel/--no-parallel", default=None, help="Run tasks in parallel")
@click.option("--verbose", "-v", is_flag=True, help="Verbose output with conversation details")
@click.option("--fail-threshold", type=float, default=0.0, help="Fail if pass rate below threshold")
@click.option("--model", "-m", type=str, help="Model to use for execution (e.g., claude-sonnet-4-20250514, gpt-4)")
@click.option("--executor", "-e", type=click.Choice(["mock", "copilot-sdk"]), help="Executor type")
@click.option("--log", "-l", type=click.Path(), help="Save full conversation transcript to JSON file")
@click.option("--context-dir", "-c", type=click.Path(exists=True), help="Directory with project files to use as context")
@click.option("--suggestions", "-s", is_flag=True, help="Generate LLM-powered improvement suggestions for failed tasks")
@click.option("--suggestions-file", type=click.Path(), help="Save suggestions to markdown file (implies --suggestions)")
@click.option("--no-issues", is_flag=True, help="Skip GitHub issue creation prompt")
def run(
    eval_path: str,
    task: tuple[str, ...],
    output: str | None,
    format: str,
    trials: int | None,
    parallel: bool | None,
    verbose: bool,
    fail_threshold: float,
    model: str | None,
    executor: str | None,
    log: str | None,
    context_dir: str | None,
    suggestions: bool,
    suggestions_file: str | None,
    no_issues: bool,
):
    """Run an evaluation suite.

    EVAL_PATH: Path to the eval.yaml file
    """
    # --suggestions-file implies --suggestions
    if suggestions_file:
        suggestions = True

    console.print(f"[bold blue]waza[/bold blue] v{__version__}")
    console.print()

    # Load spec
    try:
        spec = EvalSpec.from_file(eval_path)
        console.print(f"[green]✓[/green] Loaded eval: [bold]{spec.name}[/bold]")
        console.print(f"  Skill: {spec.skill}")
    except Exception as e:
        console.print(f"[red]✗ Failed to load eval spec:[/red] {e}")
        sys.exit(1)

    # Apply overrides
    if trials:
        spec.config.trials_per_task = trials
    if parallel is not None:
        spec.config.parallel = parallel
    if model:
        spec.config.model = model
    if executor:
        from waza.schemas.eval_spec import ExecutorType
        spec.config.executor = ExecutorType(executor)
    spec.config.verbose = verbose

    # Validate context-dir if provided
    if context_dir:
        context_path = Path(context_dir)
        if not context_path.exists():
            console.print(f"[red]✗ Context directory not found:[/red] {context_dir}")
            sys.exit(1)

        # Check for files
        file_count = sum(1 for _ in context_path.glob("**/*") if _.is_file())
        if file_count == 0:
            console.print(f"[yellow]⚠ Warning: Context directory is empty:[/yellow] {context_dir}")
            console.print("  The skill will see an empty workspace. Consider adding project files.")
        else:
            # Count relevant files
            relevant_exts = ["*.py", "*.js", "*.ts", "*.json", "*.yaml", "*.yml", "*.md", "*.txt"]
            relevant_count = sum(
                1 for ext in relevant_exts
                for f in context_path.glob(f"**/{ext}")
                if f.is_file()
            )
            console.print(f"  Context: {context_dir} ({relevant_count} files)")

    # Display executor/model info
    console.print(f"  Executor: {spec.config.executor.value}")
    console.print(f"  Model: {spec.config.model}")

    # Create base path for task loading
    base_path = Path(eval_path).parent

    # Load and filter tasks (using temporary runner just for loading)
    try:
        temp_runner = EvalRunner(spec=spec, base_path=base_path)
        tasks = temp_runner.load_tasks()
        if task:
            tasks = [t for t in tasks if t.id in task]

        if not tasks:
            console.print("[yellow]⚠ No tasks to run[/yellow]")
            sys.exit(0)

        console.print(f"  Tasks: {len(tasks)}")
        console.print(f"  Trials per task: {spec.config.trials_per_task}")
        console.print()
    except Exception as e:
        console.print(f"[red]✗ Failed to load tasks:[/red] {e}")
        sys.exit(1)

    # Create progress display
    from rich.live import Live
    from rich.table import Table

    # Track progress state
    progress_state = {
        "current_task": "",
        "current_task_num": 0,
        "total_tasks": len(tasks),
        "current_trial": 0,
        "total_trials": spec.config.trials_per_task,
        "completed_tasks": [],
        "status": "running",
        "live_messages": [],  # For real-time verbose output
    }

    # Transcript log for --log option
    transcript_log = []

    def make_progress_table() -> Table:
        """Create the progress display table."""
        table = Table.grid(padding=(0, 1))
        table.add_column(justify="right", width=12)
        table.add_column()

        # Progress bar
        completed = len(progress_state["completed_tasks"])
        total = progress_state["total_tasks"]
        pct = (completed / total * 100) if total > 0 else 0
        bar_width = 30
        filled = int(bar_width * completed / total) if total > 0 else 0
        bar = "█" * filled + "░" * (bar_width - filled)

        table.add_row(
            "[bold]Progress:[/bold]",
            f"[green]{bar}[/green] {completed}/{total} ({pct:.0f}%)"
        )

        if progress_state["current_task"]:
            task_name = progress_state["current_task"][:50]
            trial_info = ""
            if progress_state["total_trials"] > 1:
                trial_info = f" [dim](trial {progress_state['current_trial']}/{progress_state['total_trials']})[/dim]"
            table.add_row(
                "[bold]Task:[/bold]",
                f"[cyan]{task_name}[/cyan]{trial_info}"
            )

        # Show activity indicator from live messages
        if progress_state["live_messages"]:
            messages = progress_state["live_messages"]
            # Count tools used
            tool_calls = [m for m in messages if m.get("role") == "tool"]
            tool_count = len(tool_calls)

            # Get current activity
            last_msg = messages[-1]
            role = last_msg.get("role", "")

            if role == "user":
                activity = "[cyan]→ Sending prompt...[/cyan]"
            elif role == "tool":
                tool_name = last_msg.get("name", "tool")
                activity = f"[yellow]⚙ {tool_name}[/yellow] [dim]({tool_count} tools)[/dim]"
            elif role == "assistant":
                content = last_msg.get("content", "")[:60]
                if tool_count > 0:
                    activity = f"[green]✓ Response[/green] [dim]({tool_count} tools used)[/dim]"
                else:
                    activity = f"[green]✓ {content}...[/green]" if content else "[green]✓ Generating response...[/green]"
            else:
                activity = "[dim]Processing...[/dim]"

            table.add_row("[bold]Status:[/bold]", activity)

        # Show last completed task
        if progress_state["completed_tasks"]:
            last = progress_state["completed_tasks"][-1]
            icon = "✅" if last["status"] == "passed" else "❌"
            duration = f"{last['duration_ms'] / 1000:.1f}s" if last['duration_ms'] >= 1000 else f"{last['duration_ms']}ms"
            table.add_row(
                "[bold]Last:[/bold]",
                f"{icon} {last['name'][:40]} [dim]({duration})[/dim]"
            )

        # In verbose mode, show the actual prompt being sent
        if verbose and progress_state["live_messages"]:
            for msg in progress_state["live_messages"]:
                if msg.get("role") == "user":
                    prompt_preview = msg.get("content", "")[:70]
                    table.add_row("[dim]Prompt:[/dim]", f"[dim italic]{prompt_preview}...[/dim italic]")
                    break

        return table

    def progress_callback(
        event: str,
        task_name: str | None = None,
        task_num: int | None = None,
        total_tasks: int | None = None,
        trial_num: int | None = None,
        total_trials: int | None = None,
        status: str | None = None,
        duration_ms: int | None = None,
        details: dict | None = None,
    ):
        """Handle progress updates from the runner."""
        if event == "task_start":
            progress_state["current_task"] = task_name or ""
            progress_state["current_task_num"] = task_num or 0
            progress_state["current_trial"] = 1
            progress_state["live_messages"] = []  # Clear for new task
        elif event == "trial_start":
            progress_state["current_trial"] = trial_num or 1
            progress_state["total_trials"] = total_trials or 1
            progress_state["live_messages"] = []  # Clear for new trial
        elif event == "message":
            # Real-time message from conversation
            if details:
                msg = {
                    "role": details.get("role", "unknown"),
                    "content": details.get("content", ""),
                    "name": details.get("name"),
                    "task": task_name,
                    "trial": trial_num,
                }
                progress_state["live_messages"].append(msg)
                # Log for --log option
                if log:
                    transcript_log.append({
                        "timestamp": datetime.now().isoformat(),
                        "task": task_name,
                        "trial": trial_num,
                        **msg,
                    })
        elif event == "task_complete":
            progress_state["completed_tasks"].append({
                "name": task_name,
                "status": status,
                "duration_ms": duration_ms or 0,
                "score": details.get("score", 0) if details else 0,
            })
            progress_state["current_task"] = ""
            progress_state["live_messages"] = []

    # Create runner with progress callback and context_dir
    runner = EvalRunner(
        spec=spec,
        base_path=base_path,
        progress_callback=progress_callback,
        context_dir=context_dir,
    )

    # Run with live progress display
    with Live(make_progress_table(), console=console, refresh_per_second=4) as live:
        import asyncio

        async def run_with_progress():
            while True:
                live.update(make_progress_table())
                await asyncio.sleep(0.25)

        async def run_eval():
            return await runner.run_async(tasks)

        async def main_loop():
            eval_task = asyncio.create_task(run_eval())

            # Update display while eval runs
            while not eval_task.done():
                live.update(make_progress_table())
                await asyncio.sleep(0.1)

            # Final update to show 100% completion
            progress_state["current_task"] = ""
            live.update(make_progress_table())

            return await eval_task

        result = asyncio.run(main_loop())

    # Display results
    _display_results(result, verbose)

    # Save transcript log if --log specified
    if log:
        import json
        log_data = {
            "eval_name": result.eval_name,
            "skill": result.skill,
            "timestamp": result.timestamp.isoformat(),
            "config": result.config.model_dump(),
            "transcript": transcript_log,
            "tasks": [t.model_dump() for t in result.tasks],
        }
        Path(log).write_text(json.dumps(log_data, indent=2, default=str))
        console.print(f"[green]✓[/green] Transcript logged to: {log}")

    # Output to file
    if output:
        if format == "json":
            reporter = JSONReporter()
            reporter.report_to_file(result, output)
        elif format == "markdown":
            reporter = MarkdownReporter()
            reporter.report_to_file(result, output)
        elif format == "github":
            reporter = GitHubReporter()
            Path(output).write_text(reporter.report_summary(result))

        console.print(f"\n[green]✓[/green] Results written to: {output}")

    # Generate suggestions for failed tasks if requested
    failed_tasks = [t for t in result.tasks if t.status == "failed"]
    if suggestions and failed_tasks:
        console.print()
        _generate_suggestions(result, spec, model or spec.config.model, console, suggestions_file)
    elif failed_tasks and not suggestions:
        console.print()
        console.print("[dim]💡 Tip: Use --suggestions to get LLM-powered improvement recommendations for failed tasks[/dim]")

    # Prompt for GitHub issue creation (if not disabled)
    if not no_issues and (failed_tasks or Confirm.ask("\nCreate GitHub issues with results?", default=False)):
        console.print()
        _prompt_issue_creation(result, log, suggestions_file, console)

    # Check threshold
    if result.summary.pass_rate < fail_threshold:
        console.print(f"\n[red]✗ Pass rate {result.summary.pass_rate:.1%} below threshold {fail_threshold:.1%}[/red]")
        sys.exit(1)


@main.command()
@click.argument("skill_name")
@click.option("--path", "-p", type=click.Path(), default=".", help="Output directory")
@click.option("--from-skill", "-s", type=str, help="Path or URL to SKILL.md to generate from")
def init(skill_name: str, path: str, from_skill: str | None):
    """Initialize a new eval suite for a skill.

    SKILL_NAME: Name of the skill to create evals for
    """
    output_dir = Path(path) / skill_name

    # Check if user wants to generate from SKILL.md
    if not from_skill:
        has_skill_md = Confirm.ask(
            "Do you have a SKILL.md file to generate evals from?",
            default=False
        )
        if has_skill_md:
            from_skill = Prompt.ask(
                "Enter path or URL to SKILL.md",
                default=""
            )

    # If we have a skill source, use the generator
    if from_skill and from_skill.strip():
        _generate_from_skill(from_skill.strip(), output_dir, skill_name)
        return

    # Otherwise, create template structure
    output_dir.mkdir(parents=True, exist_ok=True)

    # Create eval.yaml
    eval_yaml = f"""# Eval specification for {skill_name}
name: {skill_name}-eval
description: Evaluation suite for the {skill_name} skill
skill: {skill_name}
version: "1.0"

config:
  trials_per_task: 3
  timeout_seconds: 300
  parallel: false

metrics:
  - name: task_completion
    weight: 0.4
    threshold: 0.8
  - name: trigger_accuracy
    weight: 0.3
    threshold: 0.9
  - name: behavior_quality
    weight: 0.3
    threshold: 0.7

graders:
  - type: code
    name: output_validation
    config:
      assertions:
        - "len(output) > 0"

tasks:
  - "tasks/*.yaml"
"""
    (output_dir / "eval.yaml").write_text(eval_yaml)

    # Create tasks directory
    tasks_dir = output_dir / "tasks"
    tasks_dir.mkdir(exist_ok=True)

    # Create example task
    example_task = f"""# Example task for {skill_name}
id: {skill_name}-example-001
name: Example Task
description: Example task to test {skill_name}

inputs:
  prompt: "Example prompt for the skill"
  context: {{}}

expected:
  outcomes:
    - type: task_completed
  output_contains:
    - "expected output"
"""
    (tasks_dir / "example-task.yaml").write_text(example_task)

    # Create graders directory
    graders_dir = output_dir / "graders"
    graders_dir.mkdir(exist_ok=True)

    # Create example grader script
    grader_script = '''#!/usr/bin/env python3
"""Example grader script for custom validation."""

import json
import sys


def grade(context: dict) -> dict:
    """Grade the skill execution.

    Args:
        context: Grading context with task, output, transcript, etc.

    Returns:
        dict with score, passed, message, and optional details
    """
    output = context.get("output", "")

    # Add your custom grading logic here
    score = 1.0 if output else 0.0

    return {
        "score": score,
        "passed": score >= 0.5,
        "message": "Custom grading complete",
        "details": {
            "output_length": len(output),
        },
    }


if __name__ == "__main__":
    # Read context from stdin
    context = json.load(sys.stdin)
    result = grade(context)
    print(json.dumps(result))
'''
    (graders_dir / "custom_grader.py").write_text(grader_script)

    # Create trigger tests file
    trigger_tests = f"""# Trigger accuracy tests for {skill_name}
skill: {skill_name}

should_trigger_prompts:
  - prompt: "Use {skill_name} to do something"
    reason: "Explicit skill mention"
  - prompt: "Help me with [relevant task]"
    reason: "Relevant task request"

should_not_trigger_prompts:
  - prompt: "What's the weather like?"
    reason: "Unrelated question"
  - prompt: "Help me with [unrelated task]"
    reason: "Different domain"
"""
    (output_dir / "trigger_tests.yaml").write_text(trigger_tests)

    console.print(f"[green]✓[/green] Created eval suite at: [bold]{output_dir}[/bold]")
    console.print()
    console.print("Structure created:")
    console.print(f"  {output_dir}/")
    console.print("  ├── eval.yaml")
    console.print("  ├── trigger_tests.yaml")
    console.print("  ├── tasks/")
    console.print("  │   └── example-task.yaml")
    console.print("  └── graders/")
    console.print("      └── custom_grader.py")
    console.print()
    console.print("Next steps:")
    console.print("  1. Edit [bold]tasks/*.yaml[/bold] to add test cases")
    console.print("  2. Edit [bold]trigger_tests.yaml[/bold] for trigger accuracy tests")
    console.print(f"  3. Run: [bold]waza run {output_dir}/eval.yaml[/bold]")


def _generate_from_skill(source: str, output_dir: Path, skill_name: str):
    """Generate eval suite from a SKILL.md file."""
    from waza.generator import EvalGenerator, SkillParser

    parser = SkillParser()

    console.print("[bold blue]Parsing SKILL.md...[/bold blue]")

    try:
        # Determine if source is URL or file path
        if source.startswith(("http://", "https://")):
            skill = parser.parse_url(source)
        else:
            skill = parser.parse_file(source)

        console.print(f"[green]✓[/green] Parsed skill: [bold]{skill.name}[/bold]")
        console.print(f"  Triggers found: {len(skill.triggers)}")
        console.print(f"  CLI commands: {', '.join(skill.cli_commands[:5]) or 'none'}")
        console.print(f"  Keywords: {', '.join(skill.keywords[:5]) or 'none'}")
        console.print()

    except Exception as e:
        console.print(f"[red]✗ Failed to parse SKILL.md:[/red] {e}")
        sys.exit(1)

    # Generate eval files
    generator = EvalGenerator(skill)

    output_dir.mkdir(parents=True, exist_ok=True)
    tasks_dir = output_dir / "tasks"
    tasks_dir.mkdir(exist_ok=True)
    graders_dir = output_dir / "graders"
    graders_dir.mkdir(exist_ok=True)

    # Write eval.yaml
    eval_yaml = generator.generate_eval_yaml()
    (output_dir / "eval.yaml").write_text(eval_yaml)

    # Write trigger_tests.yaml
    trigger_tests = generator.generate_trigger_tests()
    (output_dir / "trigger_tests.yaml").write_text(trigger_tests)

    # Write example tasks
    tasks = generator.generate_example_tasks()
    for filename, content in tasks:
        (tasks_dir / filename).write_text(content)

    console.print(f"[green]✓[/green] Generated eval suite at: [bold]{output_dir}[/bold]")
    console.print()
    console.print("Structure created:")
    console.print(f"  {output_dir}/")
    console.print("  ├── eval.yaml [bold](auto-generated)[/bold]")
    console.print("  ├── trigger_tests.yaml [bold](auto-generated)[/bold]")
    console.print("  └── tasks/")
    for filename, _ in tasks:
        console.print(f"      └── {filename}")
    console.print()
    console.print("[yellow]Review and customize the generated files![/yellow]")
    console.print()
    console.print("Next steps:")
    console.print("  1. Review [bold]eval.yaml[/bold] graders and thresholds")
    console.print("  2. Add/edit [bold]tasks/*.yaml[/bold] test cases")
    console.print(f"  3. Run: [bold]waza run {output_dir}/eval.yaml[/bold]")


def _generate_single_skill(
    skill: Any,  # ParsedSkill from generator module
    skill_info: Any | None,  # SkillInfo from scanner module
    output_base: Path | None,
    force: bool,
    assist: bool,
    model: str,
    console: Console,
) -> None:
    """Generate eval for a single skill.

    Args:
        skill: Parsed skill object
        skill_info: SkillInfo object (for discovery mode)
        output_base: Base output directory (can be None)
        force: Whether to overwrite existing files
        assist: Whether to use LLM-assisted generation
        model: Model to use for assisted generation
        console: Rich console for output
    """
    from waza.generator import AssistedGenerator, EvalGenerator

    # Determine output directory
    safe_name = skill.name.lower().replace(' ', '-')
    safe_name = ''.join(c if c.isalnum() or c == '-' else '-' for c in safe_name)

    if output_base is None:
        output_dir = Path(safe_name)
    elif output_base.name == safe_name:
        output_dir = output_base
    else:
        output_dir = output_base / safe_name

    # Check for existing files
    if output_dir.exists() and not force and (output_dir / "eval.yaml").exists():
        overwrite = Confirm.ask(
            f"[yellow]eval.yaml already exists in {output_dir}. Overwrite?[/yellow]",
            default=False
        )
        if not overwrite:
            console.print(f"[yellow]Skipped {skill.name}[/yellow]")
            return

    output_dir.mkdir(parents=True, exist_ok=True)
    tasks_dir = output_dir / "tasks"
    tasks_dir.mkdir(exist_ok=True)

    # Use assisted generation if requested
    if assist:
        import asyncio

        console.print(f"[cyan]Generating {skill.name} with {model}...[/cyan]")
        console.print()

        async def run_assisted():
            assisted = AssistedGenerator(skill, model=model, console=console)
            try:
                await assisted.setup()
                return await assisted.generate_all()
            finally:
                await assisted.teardown()

        try:
            result = asyncio.run(run_assisted())
            tasks_data = result.get("tasks", [])
            fixtures_data = result.get("fixtures", [])
            graders_data = result.get("graders", [])

            if tasks_data:
                # Write LLM-generated tasks
                assisted_gen = AssistedGenerator(skill, model=model)
                for i, task in enumerate(tasks_data):
                    task_yaml = assisted_gen.format_task_yaml(task, graders_data)
                    filename = f"task-{i+1:03d}.yaml"
                    (tasks_dir / filename).write_text(task_yaml)
                console.print(f"[green]✓[/green] Created {len(tasks_data)} tasks")
            else:
                console.print("[yellow]⚠ LLM returned no tasks, using pattern-based generation[/yellow]")
                generator = EvalGenerator(skill)
                tasks = generator.generate_example_tasks()
                for filename, content in tasks:
                    (tasks_dir / filename).write_text(content)

            if fixtures_data:
                # Write LLM-generated fixtures
                fixtures_dir = output_dir / "fixtures"
                fixtures_dir.mkdir(exist_ok=True)
                for filename, content in fixtures_data:
                    file_path = fixtures_dir / filename
                    file_path.parent.mkdir(parents=True, exist_ok=True)
                    file_path.write_text(content)
                console.print(f"[green]✓[/green] Created {len(fixtures_data)} fixtures")
            else:
                console.print("[yellow]⚠ LLM returned no fixtures, using pattern-based generation[/yellow]")
                generator = EvalGenerator(skill)
                fixtures = generator.generate_fixtures()
                if fixtures:
                    fixtures_dir = output_dir / "fixtures"
                    fixtures_dir.mkdir(exist_ok=True)
                    for filename, content in fixtures:
                        file_path = fixtures_dir / filename
                        file_path.parent.mkdir(parents=True, exist_ok=True)
                        file_path.write_text(content)

        except ImportError as e:
            console.print(f"[red]✗ Copilot SDK required for --assist:[/red] {e}")
            console.print("[yellow]Falling back to pattern-based generation...[/yellow]")
            assist = False  # Fall through to pattern-based
        except Exception as e:
            console.print(f"[red]✗ Assisted generation failed:[/red] {e}")
            console.print("[yellow]Falling back to pattern-based generation...[/yellow]")
            assist = False  # Fall through to pattern-based

    if not assist:
        # Standard pattern-based generation
        console.print(f"[cyan]Generating {skill.name}...[/cyan]")
        generator = EvalGenerator(skill)

        # Write example tasks
        tasks = generator.generate_example_tasks()
        for filename, content in tasks:
            (tasks_dir / filename).write_text(content)
        console.print(f"[green]✓[/green] Created {len(tasks)} tasks")

        # Write fixtures (sample code files for testing context)
        fixtures = generator.generate_fixtures()
        if fixtures:
            fixtures_dir = output_dir / "fixtures"
            fixtures_dir.mkdir(exist_ok=True)
            for filename, content in fixtures:
                file_path = fixtures_dir / filename
                file_path.parent.mkdir(parents=True, exist_ok=True)
                file_path.write_text(content)
            console.print(f"[green]✓[/green] Created {len(fixtures)} fixtures")

    # Always write eval.yaml and trigger_tests.yaml using pattern-based generator
    generator = EvalGenerator(skill)

    # Write eval.yaml
    eval_yaml = generator.generate_eval_yaml()
    (output_dir / "eval.yaml").write_text(eval_yaml)

    # Write trigger_tests.yaml
    trigger_tests = generator.generate_trigger_tests()
    (output_dir / "trigger_tests.yaml").write_text(trigger_tests)

    console.print(f"[green]✓[/green] Generated eval suite at: [bold]{output_dir}[/bold]")


@main.command()
@click.argument("skill_source", type=str, required=False)
@click.option("--output", "-o", type=click.Path(), help="Output directory (default: skill name)")
@click.option("--force", "-f", is_flag=True, help="Overwrite existing files")
@click.option("--assist", "-a", is_flag=True, help="Use LLM to generate better tasks and fixtures")
@click.option("--model", "-m", type=str, default="claude-sonnet-4-20250514", help="Model for assisted generation")
@click.option("--repo", type=str, help="Scan GitHub repo for skills (e.g., microsoft/GitHub-Copilot-for-Azure)")
@click.option("--skill", type=str, help="Filter to specific skill by name (use with --repo or --scan)")
@click.option("--scan", is_flag=True, help="Scan current directory for skills")
@click.option("--all", "generate_all", is_flag=True, help="Generate evals for all discovered skills (no prompts)")
def generate(
    skill_source: str | None,
    output: str | None,
    force: bool,
    assist: bool,
    model: str,
    repo: str | None,
    skill: str | None,
    scan: bool,
    generate_all: bool,
):
    """Generate eval suite from a SKILL.md file or discover skills in repos.

    SKILL_SOURCE: Path or URL to SKILL.md file (optional if using --repo or --scan)

    Examples:

      # From a single SKILL.md file:
      waza generate ./skills/azure-functions/SKILL.md

      waza generate https://github.com/...azure-functions/SKILL.md

      # Scan a GitHub repo (interactive):
      waza generate --repo microsoft/GitHub-Copilot-for-Azure

      # Generate eval for a specific skill in a repo:
      waza generate --repo microsoft/GitHub-Copilot-for-Azure --skill azure-functions

      # Scan and generate all (CI-friendly):
      waza generate --repo microsoft/GitHub-Copilot-for-Azure --all --output ./evals

      # Scan current directory:
      waza generate --scan

      # Use LLM-assisted generation for better tasks:
      waza generate ./SKILL.md --assist
    """
    from waza.generator import SkillParser
    from waza.scanner import SkillInfo, SkillScanner, parse_repo_arg

    console.print(f"[bold blue]waza[/bold blue] v{__version__}")
    console.print()

    # Determine mode: single file, repo scan, or local scan
    if repo or scan:
        # Discovery mode
        scanner = SkillScanner(console=console)
        discovered_skills: list[SkillInfo] = []

        if repo:
            # Scan GitHub repo
            try:
                repo_name = parse_repo_arg(repo)
                console.print(f"Scanning GitHub repository: [bold]{repo_name}[/bold]")
                console.print()
                discovered_skills = scanner.scan_github_repo(repo_name)
            except Exception as e:
                console.print(f"[red]✗ Failed to scan repository:[/red] {e}")
                sys.exit(1)
        elif scan:
            # Scan local directory
            try:
                console.print("Scanning current directory for skills...")
                console.print()
                discovered_skills = scanner.scan_local_repo(".")
            except Exception as e:
                console.print(f"[red]✗ Failed to scan directory:[/red] {e}")
                sys.exit(1)

        if not discovered_skills:
            console.print("[yellow]No skills found.[/yellow]")
            sys.exit(0)

        # Filter by skill name if --skill provided
        if skill:
            skill_lower = skill.lower()
            matching = [s for s in discovered_skills if skill_lower in s.name.lower() or skill_lower in s.path.lower()]
            if not matching:
                console.print(f"[red]✗ No skill matching '{skill}' found.[/red]")
                console.print("Available skills:")
                for s in discovered_skills:
                    console.print(f"  - {s.name}")
                sys.exit(1)
            if len(matching) == 1:
                discovered_skills = matching
                generate_all = True  # Skip prompts for single skill
            else:
                # Multiple matches, let user pick
                console.print(f"[yellow]Multiple skills match '{skill}':[/yellow]")
                discovered_skills = matching

        console.print(f"[green]✓[/green] Found {len(discovered_skills)} skill(s)")
        console.print()

        # Select which skills to generate
        if generate_all:
            selected_skills = discovered_skills
        else:
            # Interactive selection
            from rich.prompt import Prompt as RichPrompt

            console.print("Available skills:")
            for i, skill_info in enumerate(discovered_skills, 1):
                console.print(f"  {i}. [bold]{skill_info.name}[/bold]")
                if skill_info.description:
                    desc = skill_info.description[:100] + "..." if len(skill_info.description) > 100 else skill_info.description
                    console.print(f"     {desc}")
            console.print()

            # Ask which skills to generate
            selection = RichPrompt.ask(
                "Select skills to generate (comma-separated numbers, or 'all')",
                default="all",
            )

            if selection.lower() == "all":
                selected_skills = discovered_skills
            else:
                try:
                    indices = [int(x.strip()) - 1 for x in selection.split(",")]
                    selected_skills = [discovered_skills[i] for i in indices if 0 <= i < len(discovered_skills)]
                except (ValueError, IndexError):
                    console.print("[red]✗ Invalid selection[/red]")
                    sys.exit(1)

        if not selected_skills:
            console.print("[yellow]No skills selected.[/yellow]")
            sys.exit(0)

        # Prompt for other options if not provided
        if not output and not generate_all:
            output = Prompt.ask("Output directory", default="./evals")

        if assist is False and not generate_all:
            assist = Confirm.ask("Use LLM-assisted generation?", default=True)

        # Generate evals for each selected skill
        console.print()
        console.print(f"Generating evals for {len(selected_skills)} skill(s)...")
        console.print()

        for skill_info in selected_skills:
            try:
                # Parse the skill
                parser = SkillParser()
                if skill_info.url:
                    skill = parser.parse_url(skill_info.url)
                else:
                    # Local skill - construct path
                    skill_path = Path(skill_info.path) / "SKILL.md"
                    skill = parser.parse_file(skill_path)

                # Generate for this skill
                _generate_single_skill(
                    skill=skill,
                    skill_info=skill_info,
                    output_base=Path(output) if output else None,
                    force=force,
                    assist=assist,
                    model=model,
                    console=console,
                )
                console.print()
            except Exception as e:
                console.print(f"[red]✗ Failed to generate for {skill_info.name}:[/red] {e}")
                continue

        console.print("[green]✓[/green] Generation complete!")

    else:
        # Single file mode
        if not skill_source:
            console.print("[red]✗ Error:[/red] SKILL_SOURCE required (or use --repo/--scan)")
            sys.exit(1)

        parser = SkillParser()
        console.print(f"Parsing: {skill_source[:80]}{'...' if len(skill_source) > 80 else ''}")

        try:
            # Determine if source is URL or file path
            if skill_source.startswith(("http://", "https://")):
                skill = parser.parse_url(skill_source)
            else:
                skill = parser.parse_file(skill_source)

            console.print(f"[green]✓[/green] Parsed skill: [bold]{skill.name}[/bold]")

            if skill.description:
                desc = skill.description[:150] + "..." if len(skill.description) > 150 else skill.description
                console.print(f"  Description: {desc}")

            console.print(f"  Triggers extracted: {len(skill.triggers)}")
            console.print(f"  Anti-triggers: {len(skill.anti_triggers)}")
            console.print(f"  CLI commands: {len(skill.cli_commands)}")
            console.print(f"  Keywords: {len(skill.keywords)}")
            console.print()

        except Exception as e:
            console.print(f"[red]✗ Failed to parse SKILL.md:[/red] {e}")
            sys.exit(1)

        # Determine output directory
        if output:
            output_dir = Path(output)
        else:
            safe_name = skill.name.lower().replace(' ', '-')
            safe_name = ''.join(c if c.isalnum() or c == '-' else '-' for c in safe_name)
            output_dir = Path(safe_name)

        _generate_single_skill(
            skill=skill,
            skill_info=None,
            output_base=output_dir,
            force=force,
            assist=assist,
            model=model,
            console=console,
        )


@main.command()
@click.argument("results_path", type=click.Path(exists=True))
@click.option("--format", "-f", type=click.Choice(["json", "markdown", "github"]), default="markdown", help="Output format")
def report(results_path: str, format: str):
    """Generate a report from eval results.

    RESULTS_PATH: Path to results JSON file
    """
    from waza.schemas.results import EvalResult

    result = EvalResult.from_file(results_path)

    if format == "json":
        reporter = JSONReporter()
        print(reporter.report(result))
    elif format == "markdown":
        reporter = MarkdownReporter()
        print(reporter.report(result))
    elif format == "github":
        reporter = GitHubReporter()
        print(reporter.report_summary(result))


@main.command()
def list_graders():
    """List available grader types."""

    console.print("[bold]Available Grader Types[/bold]")
    console.print()

    graders = {
        "code": "Deterministic code-based assertions",
        "regex": "Pattern matching against output",
        "tool_calls": "Validate tool call patterns",
        "script": "Run external Python script",
        "llm": "LLM-as-judge with rubric",
        "llm_comparison": "Compare output to reference using LLM",
        "human": "Requires human review",
        "human_calibration": "Human calibration for LLM graders",
    }

    table = Table()
    table.add_column("Type", style="cyan")
    table.add_column("Description")

    for grader_type, description in graders.items():
        table.add_row(grader_type, description)

    console.print(table)


@main.command()
@click.argument("results_files", nargs=-1, type=click.Path(exists=True), required=True)
@click.option("--output", "-o", type=click.Path(), help="Output file path for comparison report")
@click.option("--format", "-f", type=click.Choice(["markdown", "json"]), default="markdown", help="Output format")
def compare(results_files: tuple[str, ...], output: str | None, format: str):
    """Compare results across multiple eval runs.

    Useful for comparing different models, versions, or configurations.

    RESULTS_FILES: Two or more results JSON files to compare
    """
    from waza.schemas.results import EvalResult

    if len(results_files) < 2:
        console.print("[red]✗ Need at least 2 results files to compare[/red]")
        sys.exit(1)

    # Load all results
    results: list[EvalResult] = []
    for path in results_files:
        try:
            results.append(EvalResult.from_file(path))
        except Exception as e:
            console.print(f"[red]✗ Failed to load {path}:[/red] {e}")
            sys.exit(1)

    console.print("[bold blue]Model Comparison Report[/bold blue]")
    console.print()

    # Summary comparison table
    table = Table(title="Summary Comparison")
    table.add_column("Metric")
    for r in results:
        label = r.config.model or r.eval_name
        table.add_column(label[:20], justify="right")

    # Add rows
    table.add_row(
        "Pass Rate",
        *[f"{r.summary.pass_rate:.1%}" for r in results]
    )
    table.add_row(
        "Composite Score",
        *[f"{r.summary.composite_score:.2f}" for r in results]
    )
    table.add_row(
        "Tasks Passed",
        *[f"{r.summary.passed}/{r.summary.total_tasks}" for r in results]
    )
    table.add_row(
        "Duration",
        *[f"{r.summary.duration_ms}ms" for r in results]
    )
    table.add_row(
        "Executor",
        *[r.config.executor for r in results]
    )

    console.print(table)

    # Per-task comparison
    console.print()
    task_table = Table(title="Per-Task Comparison")
    task_table.add_column("Task")
    for r in results:
        label = r.config.model or r.eval_name
        task_table.add_column(label[:15], justify="center")

    # Get all task IDs
    all_task_ids = set()
    for r in results:
        for t in r.tasks:
            all_task_ids.add(t.id)

    for task_id in sorted(all_task_ids):
        row = [task_id[:30]]
        for r in results:
            task = next((t for t in r.tasks if t.id == task_id), None)
            if task:
                icon = "✅" if task.status == "passed" else "❌"
                score = task.aggregate.mean_score if task.aggregate else 0
                row.append(f"{icon} {score:.2f}")
            else:
                row.append("-")
        task_table.add_row(*row)

    console.print(task_table)

    # Identify winner
    best_idx = max(range(len(results)), key=lambda i: results[i].summary.composite_score)
    best = results[best_idx]
    console.print()
    console.print(f"[green]🏆 Best: {best.config.model or best.eval_name} (score: {best.summary.composite_score:.2f})[/green]")

    # Output to file
    if output:
        if format == "markdown":
            report = _generate_comparison_markdown(results)
            Path(output).write_text(report)
        elif format == "json":
            import json
            comparison = {
                "results": [r.model_dump() for r in results],
                "best_model": best.config.model,
                "best_score": best.summary.composite_score,
            }
            Path(output).write_text(json.dumps(comparison, indent=2, default=str))
        console.print(f"\n[green]✓[/green] Comparison written to: {output}")


@main.command()
@click.argument("telemetry_path", type=click.Path(exists=True))
@click.option("--output", "-o", type=click.Path(), help="Output file for analysis")
@click.option("--skill", "-s", type=str, help="Filter to specific skill")
def analyze(telemetry_path: str, output: str | None, skill: str | None):
    """Analyze runtime telemetry data.

    Convert captured session telemetry into eval-compatible format for analysis.

    TELEMETRY_PATH: Path to telemetry JSON file or directory
    """
    from waza.telemetry import TelemetryAnalyzer

    try:
        analyzer = TelemetryAnalyzer()
        analysis = analyzer.analyze_file(telemetry_path, skill_filter=skill)

        console.print("[bold blue]Runtime Telemetry Analysis[/bold blue]")
        console.print()
        console.print(f"Sessions analyzed: {analysis.get('total_sessions', 0)}")
        console.print(f"Skills invoked: {', '.join(analysis.get('skills', []))}")
        console.print()

        # Show metrics
        if "metrics" in analysis:
            table = Table(title="Runtime Metrics")
            table.add_column("Metric")
            table.add_column("Value", justify="right")

            for name, value in analysis["metrics"].items():
                table.add_row(name, str(value))

            console.print(table)

        if output:
            import json
            Path(output).write_text(json.dumps(analysis, indent=2, default=str))
            console.print(f"\n[green]✓[/green] Analysis written to: {output}")

    except ImportError:
        console.print("[yellow]⚠ Telemetry analysis requires additional setup[/yellow]")
        console.print("See docs/TELEMETRY.md for configuration instructions")
    except Exception as e:
        console.print(f"[red]✗ Analysis failed:[/red] {e}")
        sys.exit(1)


def _generate_comparison_markdown(results: list) -> str:
    """Generate markdown comparison report."""
    lines = ["# Model Comparison Report", ""]

    # Summary table
    lines.append("## Summary")
    lines.append("")
    headers = ["Metric"] + [r.config.model or r.eval_name for r in results]
    lines.append("| " + " | ".join(headers) + " |")
    lines.append("| " + " | ".join(["---"] * len(headers)) + " |")

    lines.append("| Pass Rate | " + " | ".join([f"{r.summary.pass_rate:.1%}" for r in results]) + " |")
    lines.append("| Composite Score | " + " | ".join([f"{r.summary.composite_score:.2f}" for r in results]) + " |")
    lines.append("| Tasks Passed | " + " | ".join([f"{r.summary.passed}/{r.summary.total_tasks}" for r in results]) + " |")
    lines.append("")

    # Per-task table
    lines.append("## Per-Task Results")
    lines.append("")

    all_task_ids = set()
    for r in results:
        for t in r.tasks:
            all_task_ids.add(t.id)

    headers = ["Task"] + [r.config.model or r.eval_name for r in results]
    lines.append("| " + " | ".join(headers) + " |")
    lines.append("| " + " | ".join(["---"] * len(headers)) + " |")

    for task_id in sorted(all_task_ids):
        row = [task_id]
        for r in results:
            task = next((t for t in r.tasks if t.id == task_id), None)
            if task:
                icon = "✅" if task.status == "passed" else "❌"
                row.append(icon)
            else:
                row.append("-")
        lines.append("| " + " | ".join(row) + " |")

    return "\n".join(lines)


def _generate_suggestions(result, spec, model: str, console, suggestions_file: str | None = None):
    """Generate LLM-powered improvement suggestions for failed tasks."""
    from datetime import datetime
    from pathlib import Path

    from rich.panel import Panel
    from rich.progress import Progress, SpinnerColumn, TextColumn

    failed_tasks = [t for t in result.tasks if t.status == "failed"]
    if not failed_tasks:
        return

    console.print("[bold]💡 Generating Improvement Suggestions[/bold]")
    console.print()

    try:
        import asyncio
        import contextlib
        import tempfile

        from copilot import CopilotClient

        async def get_suggestions():
            suggestions = {}

            # Setup client
            workspace = tempfile.mkdtemp(prefix="waza-suggestions-")
            client = CopilotClient({
                "cwd": workspace,
                "log_level": "error",
            })
            await client.start()

            try:
                with Progress(
                    SpinnerColumn(),
                    TextColumn("[progress.description]{task.description}"),
                    console=console,
                ) as progress:
                    task = progress.add_task("Analyzing failed tasks...", total=len(failed_tasks))

                    for failed_task in failed_tasks:
                        progress.update(task, description=f"Analyzing: {failed_task.name[:30]}...")

                        # Build context about what failed
                        grader_failures = []
                        for trial in failed_task.trials:
                            for name, gr in trial.grader_results.items():
                                if not gr.passed:
                                    grader_failures.append(f"- {name}: {gr.message[:100]}")

                        output_sample = ""
                        if failed_task.trials and failed_task.trials[0].output:
                            output_sample = failed_task.trials[0].output[:500]

                        prompt = f"""Analyze this failed skill evaluation task and suggest improvements for the SKILL being tested.

Skill: {spec.skill}
Task: {failed_task.name}
Task ID: {failed_task.id}

What failed:
{chr(10).join(grader_failures) if grader_failures else "Unknown failures"}

Skill output (sample):
{output_sample}

Based on this failure, suggest 2-3 specific improvements the skill author could make to handle this case better.
Focus on:
1. What the skill should have done differently
2. Edge cases or scenarios the skill might not handle well
3. Specific code or behavior changes that would help

Keep suggestions concise and actionable (1-2 sentences each)."""

                        # Create session and get response
                        session = await client.create_session({
                            "model": model,
                            "streaming": True,
                        })

                        output_parts: list[str] = []
                        done_event = asyncio.Event()

                        def make_handler(parts: list[str], done: asyncio.Event):
                            def handle_event(event) -> None:
                                event_type = event.type.value if hasattr(event.type, 'value') else str(event.type)
                                if event_type == "assistant.message":
                                    if hasattr(event.data, 'content') and event.data.content:
                                        parts.append(event.data.content)
                                elif event_type == "assistant.message_delta" and hasattr(event.data, 'delta_content') and event.data.delta_content:
                                    parts.append(event.data.delta_content)
                                if event_type in ("session.idle", "session.error"):
                                    done.set()
                            return handle_event

                        session.on(make_handler(output_parts, done_event))
                        await session.send({"prompt": prompt})

                        with contextlib.suppress(TimeoutError):
                            await asyncio.wait_for(done_event.wait(), timeout=60)

                        with contextlib.suppress(Exception):
                            await session.destroy()

                        response_text = "".join(output_parts)
                        suggestions[failed_task.id] = {
                            "name": failed_task.name,
                            "suggestions": response_text.strip() if response_text else "Unable to generate suggestions.",
                        }

                        progress.advance(task)
            finally:
                # Cleanup
                await client.stop()
                import shutil
                shutil.rmtree(workspace, ignore_errors=True)

            return suggestions

        suggestions = asyncio.run(get_suggestions())

        # Display suggestions in console
        for _task_id, data in suggestions.items():
            console.print(Panel(
                data["suggestions"],
                title=f"[bold]{data['name']}[/bold]",
                border_style="yellow",
            ))
            console.print()

        # Save to markdown file if requested
        if suggestions_file and suggestions:
            md_lines = [
                f"# Improvement Suggestions for {spec.skill}",
                "",
                f"Generated: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}",
                f"Model: {model}",
                f"Pass Rate: {result.summary.pass_rate:.1%} ({result.summary.passed}/{result.summary.total_tasks})",
                "",
                "---",
                "",
            ]

            for task_id, data in suggestions.items():
                md_lines.extend([
                    f"## {data['name']}",
                    "",
                    f"**Task ID:** `{task_id}`",
                    "",
                    "### Suggestions",
                    "",
                    data["suggestions"],
                    "",
                    "---",
                    "",
                ])

            Path(suggestions_file).write_text("\n".join(md_lines))
            console.print(f"[green]✓[/green] Suggestions saved to: {suggestions_file}")

    except ImportError:
        console.print("[yellow]⚠ Copilot SDK not available. Install with: pip install github-copilot-sdk[/yellow]")
    except Exception as e:
        console.print(f"[yellow]⚠ Could not generate suggestions: {e}[/yellow]")


def _prompt_issue_creation(
    result: Any,  # EvalResult from schemas.results module
    transcript_path: str | None,
    suggestions_path: str | None,
    console: Console,
) -> None:
    """Prompt user for GitHub issue creation and create issues if desired.

    Args:
        result: Evaluation result
        transcript_path: Path to transcript file (if saved)
        suggestions_path: Path to suggestions file (if saved)
        console: Rich console for output
    """
    from waza.issues import create_eval_issue
    from waza.scanner import parse_repo_arg

    # Check if there are failed tasks
    failed_count = sum(1 for t in result.tasks if t.status == "failed")

    if failed_count == 0:
        # No failures - ask if they still want to create an issue
        create = Confirm.ask("No failures. Create issue anyway?", default=False)
        if not create:
            return
        failed_only = False
    else:
        # Ask if they want to create issues
        create = Confirm.ask("Create GitHub issues with results?", default=False)
        if not create:
            return

        # Ask which tasks to include
        scope_prompt = "Create issues for: [F]ailed only, [A]ll tasks, [N]one"
        scope = Prompt.ask(scope_prompt, choices=["f", "F", "a", "A", "n", "N"], default="f")

        if scope.lower() == "n":
            return

        failed_only = scope.lower() == "f"

    # Prompt for target repository
    repo = Prompt.ask("Target repository (owner/repo)")

    # Validate repository format using parse_repo_arg
    try:
        repo = parse_repo_arg(repo)
    except ValueError as e:
        console.print(f"[red]✗ Invalid repository format:[/red] {e}")
        return

    # Create the issue
    try:
        console.print()
        console.print("[cyan]Creating GitHub issue...[/cyan]")

        issue_url = create_eval_issue(
            result=result,
            repo=repo,
            transcript_path=transcript_path,
            suggestions_path=suggestions_path,
            failed_only=failed_only,
        )

        if issue_url:
            console.print(f"[green]✓[/green] Created issue: {issue_url}")
        else:
            console.print("[yellow]⚠ Issue creation returned no URL[/yellow]")

    except Exception as e:
        console.print(f"[red]✗ Failed to create issue:[/red] {e}")


def _display_results(result, verbose: bool = False):
    """Display results in the console."""
    # Summary panel
    status = "✅ PASSED" if result.summary.pass_rate >= 0.8 else "❌ FAILED"
    status_color = "green" if result.summary.pass_rate >= 0.8 else "red"

    summary_text = f"""[bold]{status}[/bold]

Pass Rate: {result.summary.pass_rate:.1%} ({result.summary.passed}/{result.summary.total_tasks})
Composite Score: {result.summary.composite_score:.2f}
Duration: {result.summary.duration_ms}ms
"""

    console.print(Panel(summary_text, title=f"[{status_color}]{result.eval_name}[/{status_color}]", border_style=status_color))

    # Metrics table
    if result.metrics:
        console.print()
        table = Table(title="Metrics")
        table.add_column("Metric")
        table.add_column("Score", justify="right")
        table.add_column("Threshold", justify="right")
        table.add_column("Weight", justify="right")
        table.add_column("Status")

        for name, metric in result.metrics.items():
            status_icon = "✅" if metric.passed else "❌"
            table.add_row(
                name,
                f"{metric.score:.2f}",
                f"{metric.threshold:.2f}",
                f"{metric.weight:.1f}",
                status_icon
            )

        console.print(table)

    # Task results table
    console.print()
    table = Table(title="Task Results")
    table.add_column("Task")
    table.add_column("Status")
    table.add_column("Score", justify="right")
    table.add_column("Duration", justify="right")
    if verbose:
        table.add_column("Tool Calls", justify="right")

    status_icons = {"passed": "✅", "failed": "❌", "partial": "⚠️", "error": "💥"}

    for task in result.tasks:
        icon = status_icons.get(task.status, "❓")
        score = f"{task.aggregate.mean_score:.2f}" if task.aggregate else "-"
        duration = f"{task.aggregate.mean_duration_ms}ms" if task.aggregate else "-"

        if verbose:
            # Get tool calls from first trial
            tool_calls = "-"
            if task.trials:
                trial = task.trials[0]
                if trial.transcript_summary:
                    tool_calls = str(trial.transcript_summary.tool_calls)
            table.add_row(task.name[:35], icon, score, duration, tool_calls)
        else:
            table.add_row(task.name[:40], icon, score, duration)

    console.print(table)

    # Verbose: Show detailed task info including prompts and responses
    if verbose and result.tasks:
        console.print()
        console.print("[bold]Task Details[/bold]")
        console.print()

        for task in result.tasks:
            # Task header
            status_icon = status_icons.get(task.status, "❓")
            console.print(f"[bold]{status_icon} {task.name}[/bold] (id: {task.id})")

            for trial in task.trials:
                console.print(f"  [dim]Trial {trial.trial_id}:[/dim] {trial.status} | score: {trial.score:.2f} | {trial.duration_ms}ms")

                # Show transcript summary
                if trial.transcript_summary:
                    ts = trial.transcript_summary
                    if ts.tools_used:
                        console.print(f"    [dim]Tools:[/dim] {', '.join(ts.tools_used[:5])}")
                    if ts.errors:
                        console.print(f"    [red]Errors:[/red] {', '.join(ts.errors[:3])}")

                # Show conversation/transcript
                if trial.transcript:
                    console.print("    [dim]Conversation:[/dim]")
                    for _i, turn in enumerate(trial.transcript[:6]):  # Show first 6 turns
                        role = turn.get("role", "unknown")
                        content = turn.get("content", "")[:200]  # Truncate
                        if role == "user":
                            console.print(f"      [cyan]User:[/cyan] {content}")
                        elif role == "assistant":
                            console.print(f"      [green]Assistant:[/green] {content}...")
                        elif role == "tool":
                            tool_name = turn.get("name", "tool")
                            console.print(f"      [yellow]Tool ({tool_name}):[/yellow] {content[:100]}...")
                    if len(trial.transcript) > 6:
                        console.print(f"      [dim]... and {len(trial.transcript) - 6} more turns[/dim]")

                # Show grader results
                if trial.grader_results:
                    console.print("    [dim]Graders:[/dim]")
                    for name, gr in trial.grader_results.items():
                        gr_icon = "✅" if gr.passed else "❌"
                        console.print(f"      {gr_icon} {name}: {gr.score:.2f} - {gr.message[:60]}")

                # Show output snippet
                if trial.output:
                    output_preview = trial.output[:300].replace('\n', ' ')
                    console.print(f"    [dim]Output:[/dim] {output_preview}...")

                if trial.error:
                    console.print(f"    [red]Error:[/red] {trial.error}")

                console.print()


if __name__ == "__main__":
    main()
