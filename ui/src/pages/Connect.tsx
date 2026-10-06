import { Code, ConnectError } from "@connectrpc/connect";
import { useMutation } from "@connectrpc/connect-query";
import {
  Button,
  Card,
  Field,
  Input,
  makeStyles,
  MessageBar,
  MessageBarBody,
  Text,
  Title3,
  tokens,
} from "@fluentui/react-components";
import { CloudRegular } from "@fluentui/react-icons";
import { useEffect, useRef, useState } from "react";

import { useOverview } from "../api/overviewContext";
import { usePlatform } from "../app/platformContext";
import { navigate, useLocation } from "../app/router";
import { PairForm } from "../components/PairForm";
import { AgentService } from "../gen/homecloud/agent/v3/agent_pb";

const useStyles = makeStyles({
  root: {
    minHeight: "100vh",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    padding: `${tokens.spacingVerticalXXXL} ${tokens.spacingHorizontalL}`,
    boxSizing: "border-box",
  },
  card: { width: "min(480px, 100%)", padding: tokens.spacingHorizontalXXXL, gap: tokens.spacingVerticalL },
  brand: { display: "flex", alignItems: "center", gap: tokens.spacingHorizontalS },
  icon: { fontSize: "32px", color: tokens.colorBrandForeground1 },
  command: {
    fontFamily: tokens.fontFamilyMonospace,
    padding: tokens.spacingHorizontalS,
    borderRadius: tokens.borderRadiusMedium,
    backgroundColor: tokens.colorNeutralBackground3,
  },
  code: { fontFamily: tokens.fontFamilyMonospace, letterSpacing: "0.15em", textTransform: "uppercase" },
});

const codeLength = 8;

function normalize(code: string): string {
  return code.replace(/[^A-Za-z0-9]/g, "");
}

/**
 * Signs in: a browser exchanges a one-time code from `homecloud open` for a
 * session; a desktop app whose pairing with another device was revoked pairs
 * again.
 */
export function ConnectPage() {
  const platform = usePlatform();
  if (platform.kind === "desktop" && platform.connection.kind === "remote") {
    return (
      <PairAgain
        url={platform.connection.url}
        deviceName={platform.deviceName}
        pair={platform.pair}
        forget={platform.forget}
      />
    );
  }
  return <ConnectBrowser />;
}

function PairAgain({
  url,
  deviceName,
  pair,
  forget,
}: {
  url: string;
  deviceName: () => Promise<string>;
  pair: (url: string, code: string, name: string) => Promise<void>;
  forget: () => Promise<void>;
}) {
  const styles = useStyles();
  return (
    <div className={styles.root}>
      <Card className={styles.card}>
        <div className={styles.brand}>
          <CloudRegular className={styles.icon} aria-hidden />
          <Text weight="semibold" size={400}>
            homecloud
          </Text>
        </div>
        <Title3 as="h1">Pair this computer again</Title3>
        <Text>
          homecloud at {url} no longer accepts this computer: it was revoked there. On that device run{" "}
          <code>homecloud open --print</code> for a new one-time code.
        </Text>
        <PairForm initialUrl={url} deviceName={deviceName} pair={pair} />
        <Button
          appearance="subtle"
          onClick={() => {
            void forget();
          }}
        >
          Use this computer's agent instead
        </Button>
      </Card>
    </div>
  );
}

/** Pairs this browser with the agent using a one-time code from `homecloud open`. */
function ConnectBrowser() {
  const styles = useStyles();
  const { reconnect } = useOverview();
  // The code from the link `homecloud open` opens, if any.
  const linkCode = useLocation().search.get("code") ?? undefined;
  const [code, setCode] = useState(linkCode ?? "");
  const exchange = useMutation(AgentService.method.exchangeLoginCode, {
    onSuccess: () => {
      reconnect();
      navigate("/");
    },
  });
  const ready = normalize(code).length === codeLength;

  // A code in the link from `homecloud open` is exchanged once, automatically.
  const autoSubmitted = useRef(false);
  const { mutate } = exchange;
  useEffect(() => {
    if (linkCode !== undefined && normalize(linkCode).length === codeLength && !autoSubmitted.current) {
      autoSubmitted.current = true;
      mutate({ code: linkCode });
    }
  }, [linkCode, mutate]);

  const failed = exchange.isError
    ? ConnectError.from(exchange.error).code === Code.PermissionDenied
      ? "That code didn't work. It may have expired or been used already; run homecloud open again for a new one."
      : `Couldn't reach the agent: ${exchange.error.message}`
    : undefined;

  return (
    <div className={styles.root}>
      <Card className={styles.card}>
        <div className={styles.brand}>
          <CloudRegular className={styles.icon} aria-hidden />
          <Text weight="semibold" size={400}>
            homecloud
          </Text>
        </div>
        <Title3 as="h1">Connect this browser</Title3>
        <Text>
          The agent only answers apps it trusts. On the computer that runs homecloud, open a terminal and run:
        </Text>
        <Text className={styles.command}>homecloud open</Text>
        <Text size={200}>It opens this page already connected. Or enter the code it prints:</Text>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (ready) {
              exchange.mutate({ code });
            }
          }}
        >
          <Field label="One-time code" hint="Works once, until the time homecloud open printed." required>
            <Input
              className={styles.code}
              value={code}
              autoComplete="one-time-code"
              placeholder="XXXX-XXXX"
              maxLength={9}
              onChange={(_, d) => {
                setCode(d.value);
              }}
            />
          </Field>
          {failed !== undefined && (
            <MessageBar intent="error">
              <MessageBarBody>{failed}</MessageBarBody>
            </MessageBar>
          )}
          <Button appearance="primary" type="submit" disabled={!ready || exchange.isPending}>
            Connect
          </Button>
        </form>
      </Card>
    </div>
  );
}
