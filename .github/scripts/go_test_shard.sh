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

# Millisecond estimates are the mean top-level durations from successful Go CI
# runs 41096175508 and 41074210725. Unknown tests get one minute so they are
# included conservatively until the table is refreshed.
test_cost_millis() {
  case "$1" in
    TestAssuredBuiltCLIWorkflow) echo 1000 ;;
    TestAssuredCollectionRealOfflineVerifierAndRegrade) echo 104845 ;;
    TestAssuredCurrentReviewRevocationAndArmSwap) echo 56660 ;;
    TestAssuredCurrentSourceChangesRejectBeforeCollection) echo 63965 ;;
    TestAssuredFullRowsAndRegradeCannotAdoptPass) echo 30565 ;;
    TestAssuredRechecksAfterActualVerifier) echo 24640 ;;
    TestAssuredUnreviewedRemainsNonpass) echo 35805 ;;
    TestNativeArtifactPublicWireBoundBeforeOpen) echo 0 ;;
    TestNativeArtifactRejectsFIFOReplacementBeforeOpen) echo 0 ;;
    TestNativeArtifactsAreRequiredAndConfined) echo 161535 ;;
    TestNativeCollectionAcceptedAndFrozenReader) echo 134355 ;;
    TestNativeCollectionDefaultClientDoesNotAllocateCommandMockRuntime) echo 63360 ;;
    TestNativeCollectionDiscoveryOrder) echo 87400 ;;
    TestNativeCollectionObservedFailures) echo 825775 ;;
    TestNativeCollectionProductionLifecycle) echo 100200 ;;
    TestNativeCollectionPublicPayloads) echo 500285 ;;
    TestNativeFrozenResourcesReachFreshSDKWorkspace) echo 34800 ;;
    TestNativeInnerProfileRejectsFrozenPolicyMemberOracle) echo 0 ;;
    TestNativeLateFailurePreservesEveryStartedAttempt) echo 441335 ;;
    TestNativeReadSelectedDecisionCaptureVerification) echo 330620 ;;
    TestNativeReaderFrozen15cDecisionParity) echo 875875 ;;
    TestNativeReaderPreservesPrePolicyCoreFailures) echo 111705 ;;
    TestNativeReaderSuppliedDecisionProcess) echo 6290 ;;
    TestNativeSourceCurrentnessBeforeSideEffects) echo 192590 ;;
    TestNativeStaticRequirementsAndConfiguration) echo 7520 ;;
    TestNativeTerminalPolicyAndLateStop) echo 204095 ;;
    TestNativeUnsupportedPlatformBeforeEffects) echo 1000 ;;
    TestOriginalMockGuardAndLockErrorParity) echo 2910 ;;
    TestOriginalMockPolicyGoldenUnionParity) echo 3850 ;;
    TestOriginalMockSourceProjectionParity) echo 13845 ;;
    TestPlanningInputRequiresExplicitFields) echo 0 ;;
    TestReadSelectedDecisionBindsEveryCoreArtifactConsumedByAssessment) echo 270475 ;;
    TestReadSelectedDecisionRejectsCollectionReplacedAfterSelection) echo 69910 ;;
    TestReadSelectedDecisionRejectsMockCollectionReplacedAfterSelection) echo 8060 ;;
    TestSourceBoundCollection) echo 4695 ;;
    TestSourceDriftBeforeBegin) echo 1635 ;;
    TestSourcePlanBoundaries) echo 7825 ;;
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
