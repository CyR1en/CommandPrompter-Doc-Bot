import { Type } from "typebox";
import type { TSchema } from "typebox";

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function numberOption(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value)
    ? value
    : undefined;
}

function literalValue(
  value: unknown,
): value is string | number | boolean | null {
  return (
    value === null ||
    typeof value === "string" ||
    typeof value === "number" ||
    typeof value === "boolean"
  );
}

function compileLiteral(value: string | number | boolean | null): TSchema {
  return value === null ? Type.Null() : Type.Literal(value);
}

export function compileJsonSchema(value: unknown): TSchema {
  if (!record(value)) throw new Error("JSON schema must be an object");
  if (Object.hasOwn(value, "const")) {
    if (!literalValue(value.const))
      throw new Error("JSON schema const is invalid");
    return compileLiteral(value.const);
  }
  if (
    Array.isArray(value.enum) &&
    value.enum.length > 0 &&
    value.enum.every(literalValue)
  ) {
    return Type.Union(value.enum.map(compileLiteral));
  }
  if (Array.isArray(value.type)) {
    return Type.Union(
      value.type.map((item) => compileJsonSchema({ ...value, type: item })),
    );
  }
  switch (value.type) {
    case "object": {
      const rawProperties = record(value.properties) ? value.properties : {};
      const required = new Set(
        Array.isArray(value.required)
          ? value.required.filter((item) => typeof item === "string")
          : [],
      );
      const properties: Record<string, TSchema> = {};
      for (const [name, child] of Object.entries(rawProperties)) {
        const schema = compileJsonSchema(child);
        properties[name] = required.has(name) ? schema : Type.Optional(schema);
      }
      return Type.Object(properties, {
        additionalProperties: value.additionalProperties === true,
      });
    }
    case "array": {
      const minItems = numberOption(value.minItems);
      const maxItems = numberOption(value.maxItems);
      return Type.Array(compileJsonSchema(value.items), {
        ...(minItems === undefined ? {} : { minItems }),
        ...(maxItems === undefined ? {} : { maxItems }),
      });
    }
    case "string": {
      const minLength = numberOption(value.minLength);
      const maxLength = numberOption(value.maxLength);
      return Type.String({
        ...(minLength === undefined ? {} : { minLength }),
        ...(maxLength === undefined ? {} : { maxLength }),
        ...(typeof value.pattern === "string"
          ? { pattern: value.pattern }
          : {}),
      });
    }
    case "integer": {
      const minimum = numberOption(value.minimum);
      const maximum = numberOption(value.maximum);
      return Type.Integer({
        ...(minimum === undefined ? {} : { minimum }),
        ...(maximum === undefined ? {} : { maximum }),
      });
    }
    case "number": {
      const minimum = numberOption(value.minimum);
      const maximum = numberOption(value.maximum);
      return Type.Number({
        ...(minimum === undefined ? {} : { minimum }),
        ...(maximum === undefined ? {} : { maximum }),
      });
    }
    case "boolean":
      return Type.Boolean();
    case "null":
      return Type.Null();
    default:
      throw new Error("unsupported JSON schema construct");
  }
}
