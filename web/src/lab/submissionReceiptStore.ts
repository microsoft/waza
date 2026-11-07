import type { SubmissionResponse } from "../api/submissions";

export const maxSubmissionReceipts = 32;
const maxReceiptBytes = 4 * 1024 * 1024;

export interface SubmissionReceipt {
  readonly sequence: number;
  readonly json: string;
}

// Page-scoped, not persisted to disk: Lab navigation must not discard receipts.
let receipts: readonly SubmissionReceipt[] = Object.freeze([]);
let bytes = 0;
let sequence = 0;
const listeners = new Set<() => void>();

export function subscribeSubmissionReceipts(listener: () => void): () => void {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}

export function getSubmissionReceipts(): readonly SubmissionReceipt[] {
  return receipts;
}

export function submissionReceiptsFull(): boolean {
  return receipts.length >= maxSubmissionReceipts || bytes >= maxReceiptBytes;
}

export function rememberSubmission(response: SubmissionResponse): void {
  if (submissionReceiptsFull()) throw new Error("Submission receipt capacity is full. Export or explicitly clear the historical view before submitting again.");
  bytes += new TextEncoder().encode(response.json).byteLength;
  receipts = Object.freeze([...receipts, Object.freeze({ sequence: ++sequence, json: response.json })]);
  for (const listener of listeners) listener();
}

export function clearSubmissionReceipts(): void {
  receipts = Object.freeze([]);
  bytes = 0;
  for (const listener of listeners) listener();
}
