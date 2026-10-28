import canonical from '../../../../internal/runneridentity/testdata/vectors.json';
import producer from '../../../../internal/runnerunit/testdata/identities.json';

export function getStaticPaths() {
  return [
    { params: { fixture: 'canonical' }, props: { vectors: canonical } },
    { params: { fixture: 'producer' }, props: { vectors: producer } },
  ];
}

export function GET({ props }: { props: { vectors: unknown } }) {
  return new Response(JSON.stringify(props.vectors), {
    headers: { 'Content-Type': 'application/json' },
  });
}
