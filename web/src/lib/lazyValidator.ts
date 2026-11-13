import type { ValidateFunction } from "ajv";

// Ajv code generation is expensive, so schemas compile on first use instead of when a route chunk loads.
export function lazy<T>(create: () => T): () => T {
  let created = false;
  let value: T;
  return () => {
    if (!created) {
      value = create();
      created = true;
    }
    return value;
  };
}

export function lazyValidator<T>(compile: () => ValidateFunction<T>): (value: unknown) => value is T {
  const validator = lazy(compile);
  return (value: unknown): value is T => validator()(value);
}
