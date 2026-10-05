import { Toast, ToastBody, ToastTitle, useToastController, type ToastIntent } from "@fluentui/react-components";

export const toasterId = "homecloud";

/** Shows a short message in the app's toaster. */
export function useNotify(): (intent: ToastIntent, title: string, body?: string) => void {
  const { dispatchToast } = useToastController(toasterId);
  return (intent, title, body) => {
    dispatchToast(
      <Toast>
        <ToastTitle>{title}</ToastTitle>
        {body !== undefined && <ToastBody>{body}</ToastBody>}
      </Toast>,
      { intent },
    );
  };
}
