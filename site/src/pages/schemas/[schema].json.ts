import type { APIRoute } from 'astro';
import request from '../../../../schemas/runner-request-1.0.schema.json';
import negotiation from '../../../../schemas/runner-negotiation-request-1.0.schema.json';
import response from '../../../../schemas/runner-negotiation-response-1.0.schema.json';
import capabilities from '../../../../schemas/runner-capabilities-1.0.schema.json';
import unit from '../../../../schemas/runner-unit-1.0.schema.json';

export function getStaticPaths() {
  return [
    { params: { schema: 'runner-request-1.0.schema' }, props: { schema: request } },
    { params: { schema: 'runner-negotiation-request-1.0.schema' }, props: { schema: negotiation } },
    { params: { schema: 'runner-negotiation-response-1.0.schema' }, props: { schema: response } },
    { params: { schema: 'runner-capabilities-1.0.schema' }, props: { schema: capabilities } },
    { params: { schema: 'runner-unit-1.0.schema' }, props: { schema: unit } },
  ];
}

export const GET: APIRoute = ({ props }) =>
  new Response(JSON.stringify(props.schema), { headers: { 'Content-Type': 'application/schema+json' } });
