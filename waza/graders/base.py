"""Base grader interface and types."""

from __future__ import annotations

from abc import ABC, abstractmethod
from enum import StrEnum
from typing import TYPE_CHECKING, Any

from pydantic import BaseModel, Field

if TYPE_CHECKING:
    from waza.schemas.results import GraderResult


class GraderType(StrEnum):
    """Types of graders available."""

    CODE = "code"
    LLM = "llm"
    HUMAN = "human"


class GraderContext(BaseModel):
    """Context passed to graders during evaluation."""

    task: dict[str, Any] = Field(..., description="The task being graded")
    transcript: list[dict[str, Any]] = Field(
        default_factory=list, description="Execution transcript"
    )
    output: str = Field(default="", description="Final output from skill")
    outcome: dict[str, Any] = Field(default_factory=dict, description="Outcome state")
    duration_ms: int = Field(default=0, description="Execution duration")
    metadata: dict[str, Any] = Field(default_factory=dict, description="Additional metadata")


class Grader(ABC):
    """Abstract base class for all graders."""

    def __init__(self, name: str, config: dict[str, Any] | None = None):
        """Initialize grader.

        Args:
            name: Unique name for this grader instance
            config: Optional configuration dictionary
        """
        self.name = name
        self.config = config or {}

    @property
    @abstractmethod
    def grader_type(self) -> GraderType:
        """Return the type of this grader."""
        ...

    @abstractmethod
    def grade(self, context: GraderContext) -> GraderResult:
        """Grade the skill execution.

        Args:
            context: The grading context with transcript, output, etc.

        Returns:
            GraderResult with score, passed status, and details
        """
        ...

    def __repr__(self) -> str:
        return f"{self.__class__.__name__}(name={self.name!r}, type={self.grader_type.value})"


class GraderRegistry:
    """Registry for grader implementations."""

    _graders: dict[str, type[Grader]] = {}

    @classmethod
    def register(cls, grader_type: str) -> callable:
        """Decorator to register a grader class."""
        def decorator(grader_class: type[Grader]) -> type[Grader]:
            cls._graders[grader_type] = grader_class
            return grader_class
        return decorator

    @classmethod
    def get(cls, grader_type: str) -> type[Grader] | None:
        """Get a grader class by type."""
        return cls._graders.get(grader_type)

    @classmethod
    def create(
        cls, grader_type: str, name: str, config: dict[str, Any] | None = None
    ) -> Grader:
        """Create a grader instance by type."""
        grader_class = cls.get(grader_type)
        if grader_class is None:
            raise ValueError(f"Unknown grader type: {grader_type}")
        return grader_class(name=name, config=config)

    @classmethod
    def list_types(cls) -> list[str]:
        """List all registered grader types."""
        return list(cls._graders.keys())
