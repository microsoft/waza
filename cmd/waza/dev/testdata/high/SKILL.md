---
name: pdf-processor
description: |
  **WORKFLOW SKILL** - Process PDF files including text extraction, rotation, and merging.
  USE FOR: "extract PDF text", "rotate PDF", "merge PDFs", "split PDF pages", "convert PDF to text".
  DO NOT USE FOR: creating PDFs from scratch (use document-creator), editing PDF forms (use form-filler).
  INVOKES: pdf-tools MCP for extraction, file-system for I/O.
  FOR SINGLE OPERATIONS: Use pdf-tools MCP directly for simple extractions.
---

# PDF Processor

**WORKFLOW SKILL** - Handles complex PDF manipulation workflows.

## Instructions

1. Parse the user's request to determine the PDF operation
2. Use appropriate tools for the operation
3. Return results with status

## MCP Tools Used

| Step | Tool        | Command | Purpose               |
| ---- | ----------- | ------- | --------------------- |
| 1    | pdf-tools   | extract | Extract text from PDF |
| 2    | file-system | write   | Save output file      |

## Prerequisites

- **Required MCP tools:** `pdf-tools`, `file-system`

**CLI Fallback (if MCP unavailable):**
Use `pdftotext` command-line utility.
