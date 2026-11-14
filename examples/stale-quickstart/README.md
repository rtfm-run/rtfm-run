# hello-newhire

A tiny CLI that greets you, with an AI explainer powered by Gemini.

## Quickstart

1. Create a virtual environment:

   ```sh
   python3 -m venv .venv
   ```

2. Install the package:

   ```sh
   .venv/bin/pip install -e .
   ```

3. Say hello:

   ```sh
   .venv/bin/hello --user newhire
   ```

   You should see `Hello, newhire!`.

4. Ask Gemini to explain what just happened, using the Gemini CLI:

   ```sh
   gemini -p "Explain what a Python console_scripts entry point is"
   ```
