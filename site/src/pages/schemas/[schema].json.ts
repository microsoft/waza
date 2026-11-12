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
import checkpoint from '../../../../schemas/execution-checkpoint-1.0.schema.json';
import checkpointResult from '../../../../schemas/runner-checkpoint-result-1.0.schema.json';
import checkpointResultV11 from '../../../../schemas/runner-checkpoint-result-1.1.schema.json';
import aggregate from '../../../../schemas/manifest-aggregate-1.0.schema.json';

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
    { params: { schema: 'execution-checkpoint-1.0.schema' }, props: { schema: checkpoint } },
    { params: { schema: 'runner-checkpoint-result-1.0.schema' }, props: { schema: checkpointResult } },
    { params: { schema: 'runner-checkpoint-result-1.1.schema' }, props: { schema: checkpointResultV11 } },
    { params: { schema: 'manifest-aggregate-1.0.schema' }, props: { schema: aggregate } },
  ];
}

export const GET: APIRoute = ({ props }) =>
  new Response(JSON.stringify(props.schema), { headers: { 'Content-Type': 'application/schema+json' } });
