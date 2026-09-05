import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState, type ReactNode } from "react";
import { afterEach, expect, test } from "vitest";

import { Select } from "./Select";

const options = [
  { label: "Select a knowledge base", value: "" },
  { label: "Product docs", value: "docs" },
  { label: "Runbooks", value: "runbooks" },
  { label: "Retired archive", value: "archive", disabled: true },
];

afterEach(cleanup);

function Harness({ initial = "" }: { initial?: string }): ReactNode {
  const [value, setValue] = useState(initial);
  return (
    <label>
      Knowledge base
      <Select onChange={setValue} options={options} required value={value} />
    </label>
  );
}

function menuOption(label: string): HTMLElement {
  const menu = document.querySelector<HTMLElement>(".select-menu");
  if (menu === null) throw new Error("expected an open select menu");
  return within(menu).getByText(label);
}

function trigger(): HTMLElement {
  const element = document.querySelector<HTMLElement>(".select-trigger");
  if (element === null) throw new Error("expected a select trigger");
  return element;
}

test("the wrapping label still names the native control the form submits", () => {
  render(<Harness initial="docs" />);
  const native = screen.getByLabelText("Knowledge base");
  expect(native).toBeInstanceOf(HTMLSelectElement);
  expect(native).toBeRequired();
  expect(native).toHaveValue("docs");
  expect(trigger()).toHaveTextContent("Product docs");
});

test("the themed list opens on the trigger and commits the option that is clicked", async () => {
  const user = userEvent.setup();
  render(<Harness />);
  expect(document.querySelector(".select-menu")).toBeNull();

  await user.click(trigger());
  expect(document.querySelector(".select-menu")).not.toBeNull();

  await user.click(menuOption("Runbooks"));
  expect(screen.getByLabelText("Knowledge base")).toHaveValue("runbooks");
  expect(document.querySelector(".select-menu")).toBeNull();
});

test("arrow keys move the value, skip disabled options, and stop at the end", () => {
  render(<Harness initial="docs" />);
  const native = screen.getByLabelText("Knowledge base");

  fireEvent.keyDown(native, { key: "ArrowDown" });
  expect(native).toHaveValue("runbooks");
  expect(document.querySelector(".select-menu")).not.toBeNull();

  fireEvent.keyDown(native, { key: "ArrowDown" });
  expect(native).toHaveValue("runbooks");

  fireEvent.keyDown(native, { key: "Escape" });
  expect(document.querySelector(".select-menu")).toBeNull();
});

test("a disabled select ignores the trigger", async () => {
  const user = userEvent.setup();
  render(
    <label>
      Knowledge base
      <Select disabled onChange={() => undefined} options={options} value="docs" />
    </label>,
  );

  await user.click(trigger());
  expect(document.querySelector(".select-menu")).toBeNull();
});
