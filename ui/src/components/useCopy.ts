import { useNotify } from "./toast";

/** Copies text to the clipboard and tells the user, naming what was copied. */
export function useCopy(): (text: string, what: string) => void {
  const notify = useNotify();
  return (text, what) => {
    navigator.clipboard.writeText(text).then(
      () => {
        notify("success", `${what} copied`);
      },
      () => {
        notify("error", `Couldn't copy the ${what.toLowerCase()}`);
      },
    );
  };
}
