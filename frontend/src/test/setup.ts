import "@testing-library/jest-dom/vitest";

HTMLDialogElement.prototype.showModal = function showModal(): void {
  this.setAttribute("open", "");
};

HTMLDialogElement.prototype.close = function close(): void {
  this.removeAttribute("open");
};

window.scrollTo = () => undefined;
