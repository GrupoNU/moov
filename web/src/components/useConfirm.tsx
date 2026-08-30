import { useCallback, useRef, useState } from "react";

import { ConfirmDialog } from "./ModalDialog";

/**
 * A promise-shaped confirm, so callers keep the linear code they had.
 *
 * `window.confirm` returned a boolean synchronously, which is exactly why the
 * call sites read well: `if (!confirm(...)) return;` in the middle of an async
 * action. A React dialog is inherently asynchronous — it renders, then the
 * user answers — and rewriting every caller into a state machine with a
 * "pending action" field would be a far larger and more error-prone change
 * than the one this epic is making.
 *
 * So the promise is the bridge: `if (!(await confirm(message))) return;` is
 * the same shape, one `await` longer, and the dialog it renders is ours.
 *
 * Returns the element to render plus the asker. The element must be mounted
 * for the promise to ever settle.
 *
 * Lives in its own file so `ModalDialog.tsx` exports only components — the
 * fast-refresh rule is right that mixing the two costs hot reloading.
 */
export interface ConfirmRequest {
  readonly message: string;
  readonly title?: string | undefined;
  readonly confirmLabel?: string | undefined;
  readonly destructive?: boolean;
}

export function useConfirm(): {
  readonly confirm: (request: ConfirmRequest) => Promise<boolean>;
  readonly dialog: React.JSX.Element;
} {
  const [request, setRequest] = useState<ConfirmRequest | undefined>(undefined);
  const resolveRef = useRef<((answer: boolean) => void) | undefined>(undefined);

  const confirm = useCallback((next: ConfirmRequest): Promise<boolean> => {
    // A second ask while one is live resolves the first as cancelled, so no
    // caller is left awaiting a promise that can never settle.
    resolveRef.current?.(false);
    setRequest(next);
    return new Promise<boolean>((resolve) => {
      resolveRef.current = resolve;
    });
  }, []);

  const settle = useCallback((answer: boolean): void => {
    const resolve = resolveRef.current;
    resolveRef.current = undefined;
    setRequest(undefined);
    resolve?.(answer);
  }, []);

  /*
   * Nothing is rendered until there is something to ask.
   *
   * An always-mounted `<dialog>` is not inert: it still participates in the
   * document, and a host that renders this handle near the top of its tree
   * gets a closed dialog sitting in front of its own content. That is not
   * theoretical — mounting it unconditionally inside the settings sheet broke
   * that sheet's search box, because the element intercepted the interaction
   * before the input ever saw it.
   */
  const dialog =
    request === undefined ? (
      <></>
    ) : (
      <ConfirmDialog
        isOpen
        message={request.message}
        title={request.title}
        confirmLabel={request.confirmLabel}
        destructive={request.destructive ?? false}
        onConfirm={() => {
          settle(true);
        }}
        onCancel={() => {
          settle(false);
        }}
      />
    );

  return { confirm, dialog };
}
