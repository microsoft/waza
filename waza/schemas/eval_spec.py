"""Data models for eval specifications."""

from __future__ import annotations

from enum import StrEnum
from typing import Any

from pydantic import BaseModel, Field


class ExecutorType(StrEnum):
    """Types of executors available."""

    MOCK = "mock"
    COPILOT_SDK = "copilot-sdk"


class GraderType(StrEnum):
    """Types of graders available."""

    CODE = "code"
    REGEX = "regex"
    TOOL_CALLS = "tool_calls"
    SCRIPT = "script"
    LLM = "llm"
    LLM_COMPARISON = "llm_comparison"
    HUMAN = "human"
    HUMAN_CALIBRATION = "human_calibration"


class MetricConfig(BaseModel):
    """Configuration for a metric."""

    name: str = Field(..., description="Metric name")
    weight: float = Field(default=1.0, ge=0.0, le=1.0, description="Weight in composite score")
    threshold: float = Field(default=0.0, ge=0.0, le=1.0, description="Pass/fail threshold")
    enabled: bool = Field(default=True, description="Whether this metric is enabled")


class GraderConfig(BaseModel):
    """Configuration for a grader."""

    type: GraderType = Field(..., description="Type of grader")
    name: str = Field(..., description="Unique name for this grader")
    script: str | None = Field(default=None, description="Path to grader script (for code graders)")
    rubric: str | None = Field(default=None, description="Path to rubric file (for LLM graders)")
    model: str | None = Field(default=None, description="Model to use (for LLM graders)")
    config: dict[str, Any] = Field(default_factory=dict, description="Additional grader config")


class EvalConfig(BaseModel):
    """Runtime configuration for eval execution."""

    trials_per_task: int = Field(default=1, ge=1, description="Number of trials per task")
    timeout_seconds: int = Field(default=300, ge=1, description="Timeout per trial in seconds")
    parallel: bool = Field(default=False, description="Run tasks in parallel")
    max_workers: int = Field(default=4, ge=1, description="Max parallel workers")
    fail_fast: bool = Field(default=False, description="Stop on first failure")
    verbose: bool = Field(default=False, description="Verbose output")

    # Executor configuration
    executor: ExecutorType = Field(default=ExecutorType.MOCK, description="Executor type")
    model: str = Field(default="claude-sonnet-4-20250514", description="Model for LLM-based execution")
    skill_directories: list[str] = Field(default_factory=list, description="Skill directories for Copilot SDK")
    mcp_servers: dict[str, Any] = Field(default_factory=dict, description="MCP server configurations")


class EvalSpec(BaseModel):
    """Complete eval specification."""

    name: str = Field(..., description="Eval suite name")
    description: str = Field(default="", description="Description of what this eval tests")
    skill: str = Field(..., description="Name of the skill being evaluated")
    version: str = Field(default="1.0", description="Eval spec version")
    config: EvalConfig = Field(default_factory=EvalConfig, description="Runtime configuration")
    metrics: list[MetricConfig] = Field(default_factory=list, description="Metrics to calculate")
    graders: list[GraderConfig] = Field(default_factory=list, description="Graders to apply")
    tasks: list[str] = Field(default_factory=list, description="Task file patterns to include")

    @classmethod
    def from_yaml(cls, yaml_content: str) -> EvalSpec:
        """Parse eval spec from YAML string."""
        import yaml

        data = yaml.safe_load(yaml_content)
        return cls.model_validate(data)

    @classmethod
    def from_file(cls, path: str) -> EvalSpec:
        """Load eval spec from YAML file."""
        from pathlib import Path

        content = Path(path).read_text()
        return cls.from_yaml(content)

    def to_yaml(self) -> str:
        """Serialize eval spec to YAML string."""
        import yaml

        return yaml.dump(self.model_dump(exclude_none=True), default_flow_style=False, sort_keys=False)
