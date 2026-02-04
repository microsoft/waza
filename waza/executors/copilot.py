"""Copilot SDK executor for real integration testing.

This executor uses the github-copilot-sdk to run actual Copilot agent sessions,
providing real LLM responses for integration testing.

Prerequisites:
- Install: pip install waza[copilot]
- Copilot CLI must be installed and authenticated: https://docs.github.com/en/copilot/how-tos/set-up/install-copilot-cli

Usage:
    config:
      executor: copilot-sdk
      model: gpt-5
      skill_directories:
        - ./skills
      mcp_servers:
        azure:
          type: stdio
          command: npx
          args: ["-y", "@azure/mcp", "server", "start"]
"""

from __future__ import annotations

import asyncio
import contextlib
import os
import shutil
import tempfile
import time
from typing import TYPE_CHECKING, Any

from waza.executors.base import BaseExecutor, ExecutionResult, SessionEvent

if TYPE_CHECKING:
    from copilot import CopilotClient as CopilotClientType
    from copilot import SessionEvent as SessionEventType

# Lazy import for optional dependency
_CopilotClientClass: type[CopilotClientType] | None = None

def _get_copilot_client() -> type[CopilotClientType]:
    """Lazy load the Copilot SDK client."""
    global _CopilotClientClass
    if _CopilotClientClass is None:
        try:
            from copilot import CopilotClient
            _CopilotClientClass = CopilotClient
        except ImportError as e:
            raise ImportError(
                "Copilot SDK not installed. Install with: pip install waza[copilot]\n"
                "Or: pip install github-copilot-sdk\n"
                "Also requires Copilot CLI: https://docs.github.com/en/copilot/how-tos/set-up/install-copilot-cli"
            ) from e
    return _CopilotClientClass


class CopilotExecutor(BaseExecutor):
    """Executor using GitHub Copilot SDK for real agent sessions.

    This provides actual LLM responses and skill invocations for integration testing.
    Requires Copilot CLI to be installed and authenticated.
    """

    def __init__(
        self,
        model: str = "gpt-5",
        skill_directories: list[str] | None = None,
        mcp_servers: dict[str, Any] | None = None,
        timeout_seconds: int = 300,
        streaming: bool = True,
        **kwargs: Any,
    ):
        """Initialize Copilot executor.

        Args:
            model: Model to use for responses (e.g., "gpt-5", "claude-sonnet-4.5")
            skill_directories: Directories containing SKILL.md files
            mcp_servers: MCP server configurations
            timeout_seconds: Session timeout
            streaming: Enable streaming responses
        """
        super().__init__(model=model, **kwargs)
        self.skill_directories = skill_directories or []
        self.mcp_servers = mcp_servers or {}
        self.timeout_seconds = timeout_seconds
        self.streaming = streaming

        self._client: CopilotClientType | None = None
        self._workspace: str | None = None

    async def setup(self) -> None:
        """Initialize Copilot client."""
        client_class = _get_copilot_client()

        # Create temp workspace
        self._workspace = tempfile.mkdtemp(prefix="waza-")

        # Initialize client with workspace
        self._client = client_class({
            "cwd": self._workspace,
            "log_level": "error",
        })
        await self._client.start()

    async def teardown(self) -> None:
        """Clean up resources."""
        if self._client:
            with contextlib.suppress(Exception):
                await self._client.stop()
            self._client = None

        if self._workspace and os.path.exists(self._workspace):
            with contextlib.suppress(Exception):
                shutil.rmtree(self._workspace)
            self._workspace = None

    async def execute(
        self,
        prompt: str,
        context: dict[str, Any] | None = None,
        skill_name: str | None = None,
    ) -> ExecutionResult:
        """Execute a prompt using Copilot SDK.

        Each execution gets a fresh temp workspace with context files copied in.
        This ensures task isolation - modifications to files don't affect other tasks.
        """
        # Teardown any existing client and workspace to ensure clean state
        await self.teardown()

        # Create fresh temp workspace for this task
        self._workspace = tempfile.mkdtemp(prefix="waza-")

        # Write context files BEFORE initializing client
        if context:
            await self._setup_context(context)

        # Now initialize client with populated workspace
        client_class = _get_copilot_client()
        self._client = client_class({
            "cwd": self._workspace,
            "log_level": "error",
        })
        await self._client.start()

        start_time = time.time()
        events: list[SessionEvent] = []
        output_parts: list[str] = []
        error: str | None = None

        try:

            # Create session with model config
            session = await self._client.create_session({
                "model": self.model,
                "streaming": self.streaming,
            })

            # Set up event collection
            done_event = asyncio.Event()

            def handle_event(event: SessionEventType) -> None:
                # Convert SDK event to our SessionEvent format
                event_type = event.type.value if hasattr(event.type, 'value') else str(event.type)
                session_event = SessionEvent(
                    type=event_type,
                    data=event.data.__dict__ if hasattr(event.data, '__dict__') else {},
                )
                events.append(session_event)

                # Collect assistant messages
                if event_type == "assistant.message":
                    if hasattr(event.data, 'content') and event.data.content:
                        output_parts.append(event.data.content)
                elif event_type == "assistant.message_delta" and hasattr(event.data, 'delta_content') and event.data.delta_content:
                    output_parts.append(event.data.delta_content)

                # Check for completion
                if event_type == "session.idle":
                    done_event.set()
                elif event_type == "session.error":
                    nonlocal error
                    error = getattr(event.data, 'message', 'Unknown error')
                    done_event.set()

            # Register event handler
            session.on(handle_event)

            # Send prompt
            await session.send({"prompt": prompt})

            # Wait for completion with timeout
            try:
                await asyncio.wait_for(
                    done_event.wait(),
                    timeout=self.timeout_seconds,
                )
            except TimeoutError:
                error = f"Session timed out after {self.timeout_seconds}s"

            # Cleanup session
            with contextlib.suppress(Exception):
                await session.destroy()

        except Exception as e:
            error = str(e)

        duration_ms = int((time.time() - start_time) * 1000)
        output = "".join(output_parts)

        # Extract tool calls from events
        tool_calls = [
            {"name": getattr(e.data, 'tool_name', ''), "arguments": getattr(e.data, 'arguments', {})}
            for e in events
            if e.type == "tool.execution_start"
        ]

        return ExecutionResult(
            output=output,
            events=events,
            model=self.model,
            skill_name=skill_name,
            duration_ms=duration_ms,
            tool_calls=tool_calls,
            error=error,
            success=error is None,
        )

    async def _setup_context(self, context: dict[str, Any]) -> None:
        """Set up workspace with context files."""
        if not self._workspace:
            return

        # Handle task-level files
        files = context.get("files", [])
        for file_info in files:
            path = file_info.get("path", "")
            content = file_info.get("content", "")

            if path and content:
                full_path = os.path.join(self._workspace, path)
                dir_path = os.path.dirname(full_path)
                if dir_path:
                    os.makedirs(dir_path, exist_ok=True)
                with open(full_path, "w") as f:
                    f.write(content)

        # Handle project_files from --context-dir
        project_files = context.get("project_files", [])
        for file_info in project_files:
            path = file_info.get("path", "")
            content = file_info.get("content", "")

            if path and content:
                full_path = os.path.join(self._workspace, path)
                dir_path = os.path.dirname(full_path)
                if dir_path:
                    os.makedirs(dir_path, exist_ok=True)
                with open(full_path, "w") as f:
                    f.write(content)


def is_copilot_sdk_available() -> bool:
    """Check if Copilot SDK is installed and available."""
    try:
        _get_copilot_client()
        return True
    except ImportError:
        return False


def get_sdk_skip_reason() -> str | None:
    """Get reason why SDK tests should be skipped, or None if they can run."""
    if os.environ.get("CI") == "true":
        return "Running in CI environment"

    if os.environ.get("SKIP_INTEGRATION_TESTS") == "true":
        return "SKIP_INTEGRATION_TESTS=true"

    if not is_copilot_sdk_available():
        return "copilot-sdk not installed"

    return None
