import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";
afterEach(cleanup);
// jsdom has no native dialog presentation; preserve open/focus behavior for components.
HTMLDialogElement.prototype.showModal = function () {
  this.open = true;
  this.querySelector<HTMLElement>("[autofocus],button,input")?.focus();
};
HTMLDialogElement.prototype.close = function () {
  this.open = false;
};
