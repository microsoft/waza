package checks

import (
	"errors"

	"github.com/microsoft/waza/internal/skill"
)

// RunChecks executes each checker against sk, collecting results and errors.
func RunChecks(checkers []ComplianceChecker, sk skill.Skill) ([]*CheckResult, error) {
	var (
		errs    []error
		results []*CheckResult
	)
	for _, c := range checkers {
		r, err := c.Check(sk)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		results = append(results, r)
	}
	return results, errors.Join(errs...)
}

// SpecCheckers returns all spec compliance checkers in display order.
func SpecCheckers() []ComplianceChecker {
	return []ComplianceChecker{
		&SpecFrontmatterChecker{},
		&SpecAllowedFieldsChecker{},
		&SpecNameChecker{},
		&SpecDirMatchChecker{},
		&SpecDescriptionChecker{},
		&SpecSecurityChecker{},
		&SpecCompatibilityChecker{},
		&SpecLicenseChecker{},
		&SpecVersionChecker{},
	}
}

// AdvisoryCheckers returns all advisory checkers in display order.
func AdvisoryCheckers() []ComplianceChecker {
	return []ComplianceChecker{
		&ModuleCountChecker{},
		&ComplexityChecker{},
		&NegativeDeltaRiskChecker{},
		&ProceduralContentChecker{},
		&OverSpecificityChecker{},
		&CrossModelDensityChecker{},
		&BodyStructureChecker{},
		&ProgressiveDisclosureChecker{},
	}
}
