---
name: pdf-processor
description: |
  Process PDF files including text extraction, rotation, and merging.
  USE FOR: "extract PDF text", "rotate PDF", "merge PDFs", "split PDF pages", "convert PDF to text".
  DO NOT USE FOR: creating PDFs from scratch (use document-creator), editing PDF forms (use form-filler).
---

# PDF Processor

Handles common PDF manipulation tasks including extraction, rotation, and merging.

## Instructions

1. Parse the user's request to determine the PDF operation
2. Use appropriate tools for the operation
3. Return results with status

## Examples

**User**: Extract text from my PDF
**Response**: I'll use the pdf-tools to extract text from your document.
