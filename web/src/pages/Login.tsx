import { FormEvent, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api } from "../api";
import { usePageTitle } from "../lib/usePageTitle";
import { Icon } from "../components/Icon";
import logo from "../assets/logo.png";

export function Login() {
  usePageTitle("Log in");
  const navigate = useNavigate();
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.login(password);
      navigate("/");
    } catch {
      setError("That password didn't match. Check the server log for the current one.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-base-200 p-4">
      <div className="w-full max-w-sm">
        {/* The full lockup carries the wordmark, so no duplicate heading. */}
        <img
          src={logo}
          alt="seedstrem"
          width={176}
          height={176}
          className="mx-auto h-44 w-44 rounded-[2rem] shadow-[0_24px_60px_-30px_var(--color-primary)]"
        />
        <form className="surface mt-6 flex flex-col gap-3 p-6" onSubmit={submit} noValidate>
          <label className="flex flex-col gap-1">
            <span className="label-text mb-1">Admin password</span>
            <input
              type="password"
              className={`input input-bordered w-full ${error ? "input-error" : ""}`}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="current-password"
              aria-invalid={error ? true : undefined}
              aria-describedby="login-hint"
              autoFocus
            />
            <span id="login-hint" className="label-text-alt mt-1 text-base-content/60">
              Printed to the server log on first run.
            </span>
          </label>
          {error && (
            <div className="alert alert-error py-2 text-sm" role="alert">
              <Icon name="alert" size={16} />
              <span>{error}</span>
            </div>
          )}
          <button className="btn btn-primary" disabled={busy || !password}>
            {busy ? <span className="loading loading-spinner loading-sm" /> : "Log in"}
          </button>
        </form>
      </div>
    </div>
  );
}
