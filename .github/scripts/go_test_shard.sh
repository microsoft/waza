#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ] || [[ ! "$1" =~ ^[0-3]$ ]] || [ -z "$2" ]; then
  echo "Usage: bash go_test_shard.sh <shard 0-3> <coverage profile>" >&2
  exit 2
fi

shard="$1"
profile="$2"
package=github.com/microsoft/waza/internal/controlledcomparison
inventory="$(go test -race -covermode=atomic -list . "$package")"
names="$(printf '%s\n' "$inventory" |
  awk '/^(Test|Fuzz|Example)[^[:space:]]*$/ { print }' |
  LC_ALL=C sort)"
if [ -z "$names" ]; then
  echo "No runnable native tests discovered; refusing an empty shard." >&2
  exit 1
fi

# Millisecond estimates weight the latest isolated Go CI timing seven times the
# preceding isolated run (41468090503 attempts 3 and 2). Unknown tests get one
# minute until the table is refreshed.
test_cost_millis() {
  case "$1" in
    TestAssuredBuiltCLIWorkflow) echo 1000 ;;
    TestAssuredCollectionRealOfflineVerifierAndRegrade) echo 104942 ;;
    TestAssuredCurrentReviewRevocationAndArmSwap) echo 60945 ;;
    TestAssuredCurrentSourceChangesRejectBeforeCollection) echo 52081 ;;
    TestAssuredFullRowsAndRegradeCannotAdoptPass) echo 37929 ;;
    TestAssuredRechecksAfterActualVerifier) echo 25576 ;;
    TestAssuredUnreviewedRemainsNonpass) echo 29534 ;;
    TestNativeArtifactPublicWireBoundBeforeOpen) echo 0 ;;
    TestNativeArtifactRejectsFIFOReplacementBeforeOpen) echo 0 ;;
    TestNativeArtifactsAreRequiredAndConfined) echo 161703 ;;
    TestNativeCollectionAcceptedAndFrozenReader) echo 149232 ;;
    TestNativeCollectionDefaultClientDoesNotAllocateCommandMockRuntime) echo 51198 ;;
    TestNativeCollectionDiscoveryOrder) echo 92431 ;;
    TestNativeCollectionObservedFailures) echo 826976 ;;
    TestNativeCollectionProductionLifecycle) echo 84986 ;;
    TestNativeCollectionPublicPayloads) echo 405219 ;;
    TestNativeFrozenResourcesReachFreshSDKWorkspace) echo 36056 ;;
    TestNativeInnerProfileRejectsFrozenPolicyMemberOracle) echo 0 ;;
    TestNativeLateFailurePreservesEveryStartedAttempt) echo 373644 ;;
    TestNativeReadSelectedDecisionCaptureVerification) echo 298135 ;;
    TestNativeReaderFrozen15cDecisionParity) echo 1302495 ;;
    TestNativeReaderPreservesPrePolicyCoreFailures) echo 88325 ;;
    TestNativeReaderSuppliedDecisionProcess) echo 5599 ;;
    TestNativeSourceCurrentnessBeforeSideEffects) echo 152524 ;;
    TestNativeStaticRequirementsAndConfiguration) echo 10040 ;;
    TestNativeTerminalPolicyAndLateStop) echo 145808 ;;
    TestNativeUnsupportedPlatformBeforeEffects) echo 1000 ;;
    TestOriginalMockGuardAndLockErrorParity) echo 2912 ;;
    TestOriginalMockPolicyGoldenUnionParity) echo 4828 ;;
    TestOriginalMockSourceProjectionParity) echo 13759 ;;
    TestPlanningInputRequiresExplicitFields) echo 0 ;;
    TestReadSelectedDecisionBindsEveryCoreArtifactConsumedByAssessment) echo 218540 ;;
    TestReadSelectedDecisionRejectsCollectionReplacedAfterSelection) echo 74636 ;;
    TestReadSelectedDecisionRejectsMockCollectionReplacedAfterSelection) echo 7980 ;;
    TestSourceBoundCollection) echo 4102 ;;
    TestSourceDriftBeforeBegin) echo 1770 ;;
    TestSourcePlanBoundaries) echo 7468 ;;
    *) echo 60000 ;;
  esac
}

scratch="$(mktemp -d "${TMPDIR:-/tmp}/waza-shard-select.XXXXXX")"
trap 'rm -rf "$scratch"' EXIT
printf '%s\n' "$names" > "$profile.all"
while IFS= read -r name; do
  printf '%012d\t%s\n' "$(test_cost_millis "$name")" "$name"
done <<< "$names" |
  LC_ALL=C sort -t $'\t' -k1,1nr -k2,2 > "$scratch/ranked"

loads=(0 0 0 0)
: > "$profile.assignment"
while IFS=$'\t' read -r weight name; do
  destination=0
  for candidate in 1 2 3; do
    if [ "${loads[$candidate]}" -lt "${loads[$destination]}" ]; then
      destination="$candidate"
    fi
  done
  loads[$destination]=$((loads[$destination] + 10#$weight))
  printf '%d\t%d\t%s\n' "$destination" "$((10#$weight))" "$name" >> "$profile.assignment"
done < "$scratch/ranked"
LC_ALL=C sort -t $'\t' -k3,3 "$profile.assignment" -o "$profile.assignment"
awk -F '\t' -v shard="$shard" '$1 == shard { print $3 }' "$profile.assignment" > "$profile.selected"

pattern=""
while IFS= read -r name; do
  pattern="${pattern:+$pattern|}$name"
done < "$profile.selected"
if [ -z "$pattern" ]; then
  echo "Native shard $shard has no tests; refusing to report success." >&2
  exit 1
fi

echo "Native shard $shard/4 selects $(wc -l < "$profile.selected") of $(wc -l < "$profile.all") discovered tests, examples, and fuzz targets (estimated ${loads[$shard]} ms)."
go test -v -race -timeout=30m -coverprofile="$profile" -covermode=atomic \
  -run "^($pattern)$" "$package"
