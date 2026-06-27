import React from 'react';
import ReadingProgress from '@site/src/components/ReadingProgress';

export default function Root({children}: {children: React.ReactNode}): React.JSX.Element {
  return (
    <>
      <ReadingProgress />
      {children}
    </>
  );
}
