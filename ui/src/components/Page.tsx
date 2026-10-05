import { makeStyles, Title2, tokens } from "@fluentui/react-components";
import type { ReactNode } from "react";

const useStyles = makeStyles({
  root: { display: "flex", flexDirection: "column", gap: tokens.spacingVerticalL, maxWidth: "1200px" },
  head: { display: "flex", flexWrap: "wrap", alignItems: "center", gap: tokens.spacingHorizontalM },
  title: { marginRight: "auto" },
});

/** Page scaffold: title row with optional actions, then content. */
export function Page({ title, actions, children }: { title: string; actions?: ReactNode; children: ReactNode }) {
  const styles = useStyles();
  return (
    <div className={styles.root}>
      <div className={styles.head}>
        <Title2 as="h1" className={styles.title}>
          {title}
        </Title2>
        {actions}
      </div>
      {children}
    </div>
  );
}
