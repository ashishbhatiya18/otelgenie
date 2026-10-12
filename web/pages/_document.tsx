import { Head, Html, Main, NextScript } from 'next/document';

export default function Document() {
  return (
    <Html lang="en">
      <Head>
        <meta name="description" content="A guide that takes you from a collector endpoint to working telemetry. Runs entirely in your browser." />
      </Head>
      <body>
        <Main />
        <NextScript />
      </body>
    </Html>
  );
}
