import { knowledgeMetadata } from '../utils/chat-knowledge.mjs';

export function GET() {
  return new Response(JSON.stringify(knowledgeMetadata()), {
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
  });
}
