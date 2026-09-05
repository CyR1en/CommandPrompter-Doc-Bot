import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { expect, test } from "vitest";

const styles = readFileSync(resolve(process.cwd(), "src/styles.css"), "utf8");

type Rgb = [number, number, number];

test("small warning status text meets WCAG AA contrast on paper", () => {
  const warning = parseHex(token("warning"));
  const paper = parseHex(token("paper"));
  expect(contrastRatio(warning, paper)).toBeGreaterThanOrEqual(4.5);
});

function token(name: string): string {
  const match = new RegExp(`--${name}:\\s*(#[0-9a-f]{6})`, "iu").exec(styles);
  const value = match?.[1];
  if (value === undefined) throw new Error(`missing color token: ${name}`);
  return value;
}

function parseHex(value: string): Rgb {
  const match = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/iu.exec(value);
  const red = match?.[1];
  const green = match?.[2];
  const blue = match?.[3];
  if (red === undefined || green === undefined || blue === undefined) {
    throw new Error("expected a six-digit color token");
  }
  return [
    Number.parseInt(red, 16),
    Number.parseInt(green, 16),
    Number.parseInt(blue, 16),
  ];
}

function contrastRatio(first: Rgb, second: Rgb): number {
  const light = Math.max(luminance(first), luminance(second));
  const dark = Math.min(luminance(first), luminance(second));
  return (light + 0.05) / (dark + 0.05);
}

function luminance(color: Rgb): number {
  const [red, green, blue] = color.map((channel) => {
    const value = channel / 255;
    return value <= 0.04045
      ? value / 12.92
      : ((value + 0.055) / 1.055) ** 2.4;
  });
  if (red === undefined || green === undefined || blue === undefined) {
    throw new Error("expected RGB channels");
  }
  return 0.2126 * red + 0.7152 * green + 0.0722 * blue;
}
