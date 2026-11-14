import type { APIRoute } from 'astro';
import request from '../../../../schemas/runner-request-1.0.schema.json';
import negotiation from '../../../../schemas/runner-negotiation-request-1.0.schema.json';
import response from '../../../../schemas/runner-negotiation-response-1.0.schema.json';
import capabilities from '../../../../schemas/runner-capabilities-1.0.schema.json';
import unit from '../../../../schemas/runner-unit-1.0.schema.json';
import terminal from '../../../../schemas/runner-terminal-1.0.schema.json';
import artifacts from '../../../../schemas/artifact-manifest-1.0.schema.json';
import attempts from '../../../../schemas/execution-attempt-1.0.schema.json';
import completion from '../../../../schemas/execution-completion-1.0.schema.json';
import submission from '../../../../schemas/execution-submission-1.0.schema.json';
import submissionResult from '../../../../schemas/execution-submission-result-1.0.schema.json';
import lineage from '../../../../schemas/execution-lineage-1.0.schema.json';
import lifecycle from '../../../../schemas/execution-lifecycle-1.0.schema.json';
import provenanceStatement from '../../../../schemas/provenance-statement-1.0.schema.json';
import provenanceBundle from '../../../../schemas/provenance-bundle-1.0.schema.json';
import provenanceVerification from '../../../../schemas/provenance-verification-1.0.schema.json';
import isolation from '../../../../schemas/execution-isolation-1.0.schema.json';
import telemetry from '../../../../schemas/execution-telemetry-1.0.schema.json';
import componentInventory from '../../../../schemas/component-inventory-1.0.schema.json';
import inventoryVerification from '../../../../schemas/inventory-verification-1.0.schema.json';
import checkpoint from '../../../../schemas/execution-checkpoint-1.0.schema.json';
import checkpointResult from '../../../../schemas/runner-checkpoint-result-1.0.schema.json';
import checkpointResultV11 from '../../../../schemas/runner-checkpoint-result-1.1.schema.json';
import checkpointAvailability from '../../../../schemas/runner-checkpoint-availability-1.0.schema.json';
import aggregate from '../../../../schemas/manifest-aggregate-1.0.schema.json';
import optimizerRequest from '../../../../schemas/optimizer-adapter-request-1.0.schema.json';
import optimizerResponse from '../../../../schemas/optimizer-adapter-response-1.0.schema.json';
import optimizerCapabilities from '../../../../schemas/optimizer-capabilities-1.0.schema.json';
import optimizerReport from '../../../../schemas/optimizer-report-1.0.schema.json';
import adapterDescriptor from '../../../../schemas/adapter-work-descriptor-1.0.schema.json';
import adapterTranslation from '../../../../schemas/adapter-translation-1.0.schema.json';
import adapterResult from '../../../../schemas/adapter-result-1.0.schema.json';
import executionBudget from '../../../../schemas/execution-budget-1.0.schema.json';

export function getStaticPaths() {
  return [
    { params: { schema: 'runner-request-1.0.schema' }, props: { schema: request } },
    { params: { schema: 'runner-negotiation-request-1.0.schema' }, props: { schema: negotiation } },
    { params: { schema: 'runner-negotiation-response-1.0.schema' }, props: { schema: response } },
    { params: { schema: 'runner-capabilities-1.0.schema' }, props: { schema: capabilities } },
    { params: { schema: 'runner-unit-1.0.schema' }, props: { schema: unit } },
    { params: { schema: 'runner-terminal-1.0.schema' }, props: { schema: terminal } },
    { params: { schema: 'artifact-manifest-1.0.schema' }, props: { schema: artifacts } },
    { params: { schema: 'execution-attempt-1.0.schema' }, props: { schema: attempts } },
    { params: { schema: 'execution-completion-1.0.schema' }, props: { schema: completion } },
    { params: { schema: 'execution-submission-1.0.schema' }, props: { schema: submission } },
    { params: { schema: 'execution-submission-result-1.0.schema' }, props: { schema: submissionResult } },
    { params: { schema: 'execution-lineage-1.0.schema' }, props: { schema: lineage } },
    { params: { schema: 'execution-lifecycle-1.0.schema' }, props: { schema: lifecycle } },
    { params: { schema: 'provenance-statement-1.0.schema' }, props: { schema: provenanceStatement } },
    { params: { schema: 'provenance-bundle-1.0.schema' }, props: { schema: provenanceBundle } },
    { params: { schema: 'provenance-verification-1.0.schema' }, props: { schema: provenanceVerification } },
    { params: { schema: 'execution-isolation-1.0.schema' }, props: { schema: isolation } },
    { params: { schema: 'execution-telemetry-1.0.schema' }, props: { schema: telemetry } },
    { params: { schema: 'component-inventory-1.0.schema' }, props: { schema: componentInventory } },
    { params: { schema: 'inventory-verification-1.0.schema' }, props: { schema: inventoryVerification } },
    { params: { schema: 'execution-checkpoint-1.0.schema' }, props: { schema: checkpoint } },
    { params: { schema: 'runner-checkpoint-result-1.0.schema' }, props: { schema: checkpointResult } },
    { params: { schema: 'runner-checkpoint-result-1.1.schema' }, props: { schema: checkpointResultV11 } },
    { params: { schema: 'runner-checkpoint-availability-1.0.schema' }, props: { schema: checkpointAvailability } },
    { params: { schema: 'manifest-aggregate-1.0.schema' }, props: { schema: aggregate } },
    { params: { schema: 'optimizer-adapter-request-1.0.schema' }, props: { schema: optimizerRequest } },
    { params: { schema: 'optimizer-adapter-response-1.0.schema' }, props: { schema: optimizerResponse } },
    { params: { schema: 'optimizer-capabilities-1.0.schema' }, props: { schema: optimizerCapabilities } },
    { params: { schema: 'optimizer-report-1.0.schema' }, props: { schema: optimizerReport } },
    { params: { schema: 'adapter-work-descriptor-1.0.schema' }, props: { schema: adapterDescriptor } },
    { params: { schema: 'adapter-translation-1.0.schema' }, props: { schema: adapterTranslation } },
    { params: { schema: 'adapter-result-1.0.schema' }, props: { schema: adapterResult } },
    { params: { schema: 'execution-budget-1.0.schema' }, props: { schema: executionBudget } },
  ];
}

export const GET: APIRoute = ({ props }) =>
  new Response(JSON.stringify(props.schema), { headers: { 'Content-Type': 'application/schema+json' } });
