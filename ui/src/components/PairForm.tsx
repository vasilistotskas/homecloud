import { Code, ConnectError } from "@connectrpc/connect";
import { Button, Field, Input, makeStyles, MessageBar, MessageBarBody, tokens } from "@fluentui/react-components";
import { useEffect, useState } from "react";

import { remoteUrl } from "../remoteUrl";

const useStyles = makeStyles({
  form: { display: "flex", flexDirection: "column", gap: tokens.spacingVerticalM },
  code: { fontFamily: tokens.fontFamilyMonospace, letterSpacing: "0.15em", textTransform: "uppercase" },
});

/** Why pairing failed, in words. */
function pairError(err: unknown): string {
  const ce = ConnectError.from(err);
  switch (ce.code) {
    case Code.PermissionDenied:
      return "That code didn't work. It may have expired or been used already; run homecloud pair there for a new one.";
    case Code.ResourceExhausted:
      return "Too many wrong codes were tried there; wait a minute, then try again.";
    case Code.InvalidArgument:
      return ce.rawMessage;
    case Code.Unknown:
    case Code.Unavailable:
      return `Couldn't reach homecloud there (${ce.rawMessage}). Is Tailscale connected on this computer, and is homecloud running?`;
    default:
      return ce.rawMessage;
  }
}

/**
 * Pairs this computer with a homecloud on another device: its address, a
 * one-time code from `homecloud pair` there, and how this computer
 * is listed.
 */
export function PairForm({
  initialUrl,
  deviceName,
  pair,
}: {
  initialUrl: string;
  deviceName: () => Promise<string>;
  pair: (url: string, code: string, name: string) => Promise<void>;
}) {
  const styles = useStyles();
  const [address, setAddress] = useState(initialUrl);
  const [code, setCode] = useState("");
  const [name, setName] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string>();

  useEffect(() => {
    let current = true;
    void deviceName().then((n) => {
      if (current) {
        setName((typed) => (typed === "" ? n : typed));
      }
    });
    return () => {
      current = false;
    };
  }, [deviceName]);

  const ready = address.trim() !== "" && code.replace(/[^A-Za-z0-9]/g, "").length === 8 && name.trim() !== "";
  return (
    <form
      className={styles.form}
      onSubmit={(e) => {
        e.preventDefault();
        if (!ready) {
          return;
        }
        setPending(true);
        setError(undefined);
        pair(remoteUrl(address), code, name.trim()).catch((err: unknown) => {
          setPending(false);
          setError(pairError(err));
        });
      }}
    >
      <Field label="Address" hint="Where homecloud runs on your tailnet, e.g. homecloud.tail1234.ts.net" required>
        <Input
          value={address}
          placeholder="homecloud.tail1234.ts.net"
          onChange={(_, d) => {
            setAddress(d.value);
          }}
        />
      </Field>
      <Field label="Pairing code" hint="From homecloud pair on that device. Works once." required>
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
      <Field label="This computer's name" hint="How it is listed under Paired devices there." required>
        <Input
          value={name}
          maxLength={64}
          onChange={(_, d) => {
            setName(d.value);
          }}
        />
      </Field>
      {error !== undefined && (
        <MessageBar intent="error">
          <MessageBarBody>{error}</MessageBarBody>
        </MessageBar>
      )}
      <Button appearance="primary" type="submit" disabled={!ready || pending}>
        {pending ? "Pairing…" : "Pair"}
      </Button>
    </form>
  );
}
