import { readFileSync } from "node:fs";

const wireSchema: unknown = JSON.parse(
  readFileSync(new URL("../../wire.schema.json", import.meta.url), "utf8"),
);

function record(value: unknown): Record<string, unknown> | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value))
    return undefined;
  return Object.fromEntries(Object.entries(value));
}

function equal(left: unknown, right: unknown): boolean {
  return JSON.stringify(left) === JSON.stringify(right);
}

function resolveReference(reference: string): unknown {
  if (!reference.startsWith("#/$defs/")) return undefined;
  const root = record(wireSchema);
  const definitions = record(root?.$defs);
  return definitions?.[reference.slice("#/$defs/".length)];
}

function check(schemaValue: unknown, value: unknown): boolean {
  if (schemaValue === true) return true;
  if (schemaValue === false) return false;
  const schema = record(schemaValue);
  if (!schema) return false;
  if (typeof schema.$ref === "string")
    return check(resolveReference(schema.$ref), value);
  if (Array.isArray(schema.oneOf))
    return schema.oneOf.filter((branch) => check(branch, value)).length === 1;
  if ("const" in schema && !equal(schema.const, value)) return false;
  if (
    Array.isArray(schema.enum) &&
    !schema.enum.some((candidate) => equal(candidate, value))
  )
    return false;

  switch (schema.type) {
    case undefined:
      break;
    case "object": {
      const object = record(value);
      if (!object) return false;
      const names = Object.keys(object);
      if (
        typeof schema.maxProperties === "number" &&
        names.length > schema.maxProperties
      )
        return false;
      if (
        Array.isArray(schema.required) &&
        schema.required.some(
          (name) => typeof name !== "string" || !(name in object),
        )
      )
        return false;
      const properties = record(schema.properties) ?? {};
      for (const [name, item] of Object.entries(object)) {
        if (name in properties) {
          if (!check(properties[name], item)) return false;
        } else if (schema.additionalProperties === false) {
          return false;
        } else if (
          record(schema.additionalProperties) &&
          !check(schema.additionalProperties, item)
        ) {
          return false;
        }
      }
      break;
    }
    case "array":
      if (!Array.isArray(value)) return false;
      if (typeof schema.maxItems === "number" && value.length > schema.maxItems)
        return false;
      if (
        schema.items !== undefined &&
        !value.every((item) => check(schema.items, item))
      )
        return false;
      break;
    case "string":
      if (typeof value !== "string") return false;
      if (
        typeof schema.minLength === "number" &&
        [...value].length < schema.minLength
      )
        return false;
      if (
        typeof schema.maxLength === "number" &&
        [...value].length > schema.maxLength
      )
        return false;
      if (
        typeof schema.pattern === "string" &&
        !new RegExp(schema.pattern).test(value)
      )
        return false;
      break;
    case "integer":
      if (typeof value !== "number" || !Number.isInteger(value)) return false;
      break;
    case "number":
      if (typeof value !== "number" || !Number.isFinite(value)) return false;
      break;
    case "boolean":
      if (typeof value !== "boolean") return false;
      break;
    case "null":
      if (value !== null) return false;
      break;
    default:
      return false;
  }
  if (typeof value === "number") {
    if (typeof schema.minimum === "number" && value < schema.minimum)
      return false;
    if (typeof schema.maximum === "number" && value > schema.maximum)
      return false;
  }
  return true;
}

export function checkCanonicalWireMessage(value: unknown): boolean {
  return check(wireSchema, value);
}
