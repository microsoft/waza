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

# Millisecond estimates are the maximum top-level durations observed across
# successful runs 41096175508, 41074210725, 41462829641, and 41462830074.
# Unknown tests get one minute until the table is refreshed.
test_cost_millis() {
  case "$1" in
    TestAssuredBuiltCLIWorkflow) echo 1000 ;;
    TestAssuredCollectionRealOfflineVerifierAndRegrade) echo 104930 ;;
    TestAssuredCurrentReviewRevocationAndArmSwap) echo 65990 ;;
    TestAssuredCurrentSourceChangesRejectBeforeCollection) echo 68750 ;;
    TestAssuredFullRowsAndRegradeCannotAdoptPass) echo 42090 ;;
    TestAssuredRechecksAfterActualVerifier) echo 27430 ;;
    TestAssuredUnreviewedRemainsNonpass) echo 38670 ;;
    TestNativeArtifactPublicWireBoundBeforeOpen) echo 0 ;;
    TestNativeArtifactRejectsFIFOReplacementBeforeOpen) echo 0 ;;
    TestNativeArtifactsAreRequiredAndConfined) echo 168690 ;;
    TestNativeCollectionAcceptedAndFrozenReader) echo 158250 ;;
    TestNativeCollectionDefaultClientDoesNotAllocateCommandMockRuntime) echo 72840 ;;
    TestNativeCollectionDiscoveryOrder) echo 131570 ;;
    TestNativeCollectionObservedFailures) echo 861020 ;;
    TestNativeCollectionProductionLifecycle) echo 114630 ;;
    TestNativeCollectionPublicPayloads) echo 556450 ;;
    TestNativeFrozenResourcesReachFreshSDKWorkspace) echo 48260 ;;
    TestNativeInnerProfileRejectsFrozenPolicyMemberOracle) echo 0 ;;
    TestNativeLateFailurePreservesEveryStartedAttempt) echo 484820 ;;
    TestNativeReadSelectedDecisionCaptureVerification) echo 364330 ;;
    TestNativeReaderFrozen15cDecisionParity) echo 1363230 ;;
    TestNativeReaderPreservesPrePolicyCoreFailures) echo 113610 ;;
    TestNativeReaderSuppliedDecisionProcess) echo 6950 ;;
    TestNativeSourceCurrentnessBeforeSideEffects) echo 213850 ;;
    TestNativeStaticRequirementsAndConfiguration) echo 10590 ;;
    TestNativeTerminalPolicyAndLateStop) echo 204320 ;;
    TestNativeUnsupportedPlatformBeforeEffects) echo 1000 ;;
    TestOriginalMockGuardAndLockErrorParity) echo 3330 ;;
    TestOriginalMockPolicyGoldenUnionParity) echo 5180 ;;
    TestOriginalMockSourceProjectionParity) echo 14910 ;;
    TestPlanningInputRequiresExplicitFields) echo 0 ;;
    TestReadSelectedDecisionBindsEveryCoreArtifactConsumedByAssessment) echo 297150 ;;
    TestReadSelectedDecisionRejectsCollectionReplacedAfterSelection) echo 95120 ;;
    TestReadSelectedDecisionRejectsMockCollectionReplacedAfterSelection) echo 8120 ;;
    TestSourceBoundCollection) echo 5410 ;;
    TestSourceDriftBeforeBegin) echo 1900 ;;
    TestSourcePlanBoundaries) echo 11100 ;;
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
