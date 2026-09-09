import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";

import { SAVED_FEEDBACK_MS, useSaveFeedback } from "./useSaveFeedback";

/**
 * The save receipt's timing (F-38).
 *
 * The page test covers that a tick appears in the right place. What only a
 * fake clock can cover is the part that makes it a RECEIPT rather than a
 * permanent badge — that it goes away, and that a second save within the window
 * extends it instead of being cut short by the first save's timer.
 */

function Probe({ save }: { readonly save: Promise<boolean> | undefined }): React.JSX.Element {
  const { isSaved, report } = useSaveFeedback();
  return (
    <>
      <button
        type="button"
        onClick={() => {
          if (save !== undefined) report(save);
        }}
      >
        save
      </button>
      {isSaved && <span>saved</span>}
    </>
  );
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

/** Clicks and lets the save promise settle, with timers still faked. */
async function fire(): Promise<void> {
  await act(async () => {
    screen.getByRole("button", { name: "save" }).click();
    await Promise.resolve();
  });
}

describe("useSaveFeedback", () => {
  it("shows the tick when the save resolves true, then takes it away", async () => {
    render(<Probe save={Promise.resolve(true)} />);
    await fire();

    expect(screen.getByText("saved")).toBeInTheDocument();

    act(() => {
      vi.advanceTimersByTime(SAVED_FEEDBACK_MS);
    });

    // A tick that never left would be a badge saying "this row was once saved",
    // which is not what it means.
    expect(screen.queryByText("saved")).not.toBeInTheDocument();
  });

  it("shows nothing when the save resolves false", async () => {
    render(<Probe save={Promise.resolve(false)} />);
    await fire();

    // A failure is reported permanently by the provider's own error strip, and
    // must be: a failed preference is a state the user has to act on, so a
    // message that disappears would be a problem they are allowed to miss.
    expect(screen.queryByText("saved")).not.toBeInTheDocument();
  });

  it("extends the window on a second save rather than being cut short", async () => {
    render(<Probe save={Promise.resolve(true)} />);
    await fire();

    act(() => {
      vi.advanceTimersByTime(SAVED_FEEDBACK_MS - 200);
    });
    await fire();
    act(() => {
      vi.advanceTimersByTime(300);
    });

    // Without clearing the first timer, the second confirmation would vanish
    // 200 ms after appearing.
    expect(screen.getByText("saved")).toBeInTheDocument();
  });
});
