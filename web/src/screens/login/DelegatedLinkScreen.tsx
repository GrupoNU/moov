import { useBranding } from "../../branding/BrandingProvider";
import { useTranslation } from "../../i18n/I18nProvider";
import type { DelegatedFailure } from "../../auth/AuthProvider";
import { BrandMark } from "../../components/BrandMark";
import { LegalFooter } from "../../components/LegalFooter";
import { messageFor } from "./delegatedMessages";
import { ErrorNotice } from "./ErrorNotice";
import styles from "./LoginScreen.module.css";

/**
 * What a user sees when a delegated link cannot be used (epic M2, contract
 * §3.7).
 *
 * # Why this exists instead of the login form
 *
 * A user who arrived through a portal has no password. The mailbox was
 * created for them by the accounts API with a random password that was
 * discarded at provisioning (§2.4) — there is literally nothing they could
 * type into the form. Showing it would be worse than unhelpful: it would make
 * them believe they had forgotten a credential they never had, and send them
 * to an administrator with a question that has no answer.
 *
 * So every dead-link path lands here, and every message ends with the one
 * action that works: go back to the portal and open the mail from there.
 *
 * # Why it reuses the login screen's chrome
 *
 * Same split layout, same brand panel, same error notice. This is the second
 * face of the same door, and a user who has seen one should recognise the
 * other — a differently-shaped page would read as "wrong site" at exactly the
 * moment they are already unsure whether their link worked.
 *
 * The `not-provisioned` case deliberately borrows the EXISTING
 * not-provisioned copy rather than inventing new words for it: the situation
 * is identical (the mailbox is not set up in Moov, an administrator has to
 * add it), only the route in was different.
 */

export interface DelegatedLinkScreenProps {
  readonly reason: DelegatedFailure;
}

export function DelegatedLinkScreen({ reason }: DelegatedLinkScreenProps): React.JSX.Element {
  const translation = useTranslation();
  const branding = useBranding();

  const message = messageFor(reason, translation);

  return (
    <div className={styles.layout}>
      <main className={styles.formSide} id="main">
        <div className={styles.formCard}>
          <div className={styles.compactBrand}>
            <BrandMark branding={branding} size="sm" />
          </div>

          <header className={styles.header}>
            <h1 className={styles.heading}>{message.title}</h1>
            <p className={styles.subheading}>{message.body}</p>
          </header>

          <ErrorNotice
            id="delegated-link-error"
            title={message.title}
            body={message.body}
            {...(message.suggestsAdministrator && branding.supportUrl !== ""
              ? { supportUrl: branding.supportUrl }
              : {})}
            showAdministratorHint={message.suggestsAdministrator}
          />

          <LegalFooter placement="login" />
        </div>
      </main>
    </div>
  );
}
