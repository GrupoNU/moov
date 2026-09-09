import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { I18nProvider } from "../../i18n/I18nProvider";
import { en } from "../../i18n/strings";
import { OptionGroup } from "./OptionGroup";
import { ReadingPaneThumb } from "./QuickThumbnails";

/**
 * The shared radio group (F-26, F-34, F-35).
 *
 * What these pin is the part the review was actually about: that a two- or
 * three-option setting is a group you can COMPARE — every option visible at
 * once, each with its explanation — rather than a select showing one of them.
 * And, because the explanation is the new thing, that the sentence is a
 * DESCRIPTION and not part of the radio's name: a radio announced as "Reply
 * answers the sender only" has a name no user would ever say out loud.
 */

type Reply = "reply" | "replyAll";

function renderGroup({ showLegend = true }: { readonly showLegend?: boolean } = {}) {
  const onChange = vi.fn<(next: Reply) => void>();
  render(
    <I18nProvider locale="en">
      <OptionGroup<Reply>
        legendKey="settings.replyBehavior.label"
        showLegend={showLegend}
        value="reply"
        options={["reply", "replyAll"]}
        labelKey={(option) =>
          option === "reply"
            ? "settings.replyBehavior.reply"
            : "settings.replyBehavior.replyAll"
        }
        describeKey={(option) =>
          option === "reply"
            ? "settings.replyBehavior.replyNote"
            : "settings.replyBehavior.replyAllNote"
        }
        variant="inline"
        onChange={onChange}
      />
    </I18nProvider>,
  );
  return { onChange };
}

describe("the option group", () => {
  it("is a named group of real radios, so every option is visible at once", () => {
    renderGroup();

    const group = screen.getByRole("group", { name: en["settings.replyBehavior.label"] });
    expect(within(group).getAllByRole("radio")).toHaveLength(2);
    expect(
      within(group).getByRole("radio", { name: en["settings.replyBehavior.reply"] }),
    ).toBeChecked();
  });

  it("names a radio by its LABEL and describes it with the explanation (F-26)", () => {
    renderGroup();

    // The name is the word a user would speak. The sentence is announced after
    // it, as a description, at the verbosity the user chose.
    const radio = screen.getByRole("radio", { name: en["settings.replyBehavior.reply"] });
    const describedBy = radio.getAttribute("aria-describedby");
    expect(describedBy).not.toBeNull();
    expect(document.getElementById(describedBy ?? "")).toHaveTextContent(
      en["settings.replyBehavior.replyNote"],
    );
  });

  it("reports the option the user picked", async () => {
    const user = userEvent.setup();
    const { onChange } = renderGroup();

    await user.click(
      screen.getByRole("radio", { name: en["settings.replyBehavior.replyAll"] }),
    );

    expect(onChange).toHaveBeenCalledWith("replyAll");
  });

  it("hides the legend on request, because the settings row already prints the name", () => {
    renderGroup({ showLegend: false });

    // Still the group's accessible name — hidden, not removed. A group with no
    // name announces its radios with no idea what they are options OF.
    const group = screen.getByRole("group", { name: en["settings.replyBehavior.label"] });
    expect(within(group).getByText(en["settings.replyBehavior.label"])).toHaveClass(
      "visually-hidden",
    );
  });

  it("carries a thumbnail beside each option when the choice is about how something looks", () => {
    render(
      <I18nProvider locale="en">
        <OptionGroup<"none" | "right" | "bottom">
          legendKey="settings.readingPane.label"
          value="right"
          options={["none", "right", "bottom"]}
          labelKey={(pane) =>
            pane === "none"
              ? "settings.readingPane.none"
              : pane === "right"
                ? "settings.readingPane.right"
                : "settings.readingPane.bottom"
          }
          onChange={() => undefined}
          renderThumb={(pane) => <ReadingPaneThumb pane={pane} />}
        />
      </I18nProvider>,
    );

    // F-35: the settings page had no previews at all. One per option, and the
    // pictures are hidden from the tree because each sits beside its own label.
    const group = screen.getByRole("group", { name: en["settings.readingPane.label"] });
    const thumbs = group.querySelectorAll('svg[aria-hidden="true"]');
    expect(thumbs).toHaveLength(3);
  });
});
