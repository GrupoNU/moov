import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";

import { I18nProvider } from "../i18n/I18nProvider";
import { ConfirmDialog, PromptDialog, useConfirm } from "./ModalDialog";

/*
 * jsdom implements <dialog> but not the top layer; `showModal` exists and
 * flips `open`, which is all these tests read.
 */

function wrap(node: React.ReactNode): React.JSX.Element {
  return <I18nProvider>{node}</I18nProvider>;
}

describe("ConfirmDialog", () => {
  it("asks the question and reports the answer", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    render(
      wrap(
        <ConfirmDialog
          isOpen
          message="Delete this forever?"
          onConfirm={onConfirm}
          onCancel={onCancel}
        />,
      ),
    );

    expect(screen.getByText("Delete this forever?")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Confirm" }));
    expect(onConfirm).toHaveBeenCalledOnce();
    expect(onCancel).not.toHaveBeenCalled();
  });

  it("reports a refusal from the cancel button", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    render(
      wrap(<ConfirmDialog isOpen message="Sure?" onConfirm={onConfirm} onCancel={onCancel} />),
    );

    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onCancel).toHaveBeenCalledOnce();
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("names the confirming button when the caller gives it a verb", () => {
    // "Delete permanently" beats "Confirm" on a destructive checkpoint: the
    // button should say what it does, not that it agrees.
    render(
      wrap(
        <ConfirmDialog
          isOpen
          message="Sure?"
          confirmLabel="Delete permanently"
          destructive
          onConfirm={vi.fn()}
          onCancel={vi.fn()}
        />,
      ),
    );
    expect(screen.getByRole("button", { name: "Delete permanently" })).toBeInTheDocument();
  });
});

describe("PromptDialog", () => {
  it("validates as the user types — the thing window.prompt cannot do", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn();
    render(
      wrap(
        <PromptDialog
          isOpen
          message="Address of the link"
          validate={(value) => (value.startsWith("javascript:") ? "Not a web address" : undefined)}
          onSubmit={onSubmit}
          onCancel={vi.fn()}
        />,
      ),
    );

    const field = screen.getByRole("textbox");
    await user.type(field, "javascript:alert(1)");
    // Nothing is said until the user commits — an error mid-first-word is
    // nagging, not helping.
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "OK" }));
    expect(screen.getByRole("alert")).toHaveTextContent("Not a web address");
    // And the invalid value never reaches the caller.
    expect(onSubmit).not.toHaveBeenCalled();
    expect(field).toHaveAttribute("aria-invalid", "true");
  });

  it("clears the error as the user fixes it, then submits", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn();
    render(
      wrap(
        <PromptDialog
          isOpen
          message="Address of the link"
          validate={(value) => (value.includes(".") ? undefined : "Needs a dot")}
          onSubmit={onSubmit}
          onCancel={vi.fn()}
        />,
      ),
    );

    const field = screen.getByRole("textbox");
    await user.type(field, "nodot");
    await user.click(screen.getByRole("button", { name: "OK" }));
    expect(screen.getByRole("alert")).toBeInTheDocument();

    await user.type(field, ".com");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "OK" }));
    expect(onSubmit).toHaveBeenCalledWith("nodot.com");
  });

  it("refuses to submit an empty value", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn();
    render(
      wrap(<PromptDialog isOpen message="Value" onSubmit={onSubmit} onCancel={vi.fn()} />),
    );

    expect(screen.getByRole("button", { name: "OK" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onSubmit).not.toHaveBeenCalled();
  });
});

describe("useConfirm", () => {
  function Host({ onAnswer }: { readonly onAnswer: (answer: boolean) => void }): React.JSX.Element {
    const { confirm, dialog } = useConfirm();
    const [asked, setAsked] = useState(false);
    return (
      <>
        <button
          type="button"
          onClick={() => {
            setAsked(true);
            void confirm({ message: "Really?" }).then(onAnswer);
          }}
        >
          ask
        </button>
        {asked && <span>asked</span>}
        {dialog}
      </>
    );
  }

  it("resolves true when confirmed and false when cancelled", async () => {
    const user = userEvent.setup();
    const onAnswer = vi.fn();
    render(wrap(<Host onAnswer={onAnswer} />));

    await user.click(screen.getByRole("button", { name: "ask" }));
    await user.click(screen.getByRole("button", { name: "Confirm" }));
    await waitFor(() => {
      expect(onAnswer).toHaveBeenCalledWith(true);
    });

    await user.click(screen.getByRole("button", { name: "ask" }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => {
      expect(onAnswer).toHaveBeenCalledWith(false);
    });
  });

  /*
   * The regression that broke the settings sheet's search box: an
   * always-mounted <dialog> is not inert, and a host rendering this handle
   * near the top of its tree got a closed dialog in front of its own content.
   */
  it("renders NOTHING until there is something to ask", () => {
    const { container } = render(wrap(<Host onAnswer={vi.fn()} />));
    expect(container.querySelector("dialog")).toBeNull();
  });

  it("mounts the dialog only while a question is live", async () => {
    const user = userEvent.setup();
    const { container } = render(wrap(<Host onAnswer={vi.fn()} />));

    await user.click(screen.getByRole("button", { name: "ask" }));
    expect(container.querySelector("dialog")).not.toBeNull();

    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => {
      expect(container.querySelector("dialog")).toBeNull();
    });
  });
});
